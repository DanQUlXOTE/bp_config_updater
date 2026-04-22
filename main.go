package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"configupdater/internal/bpclient"
	"configupdater/internal/cloner"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		server       string
		configName   string
		templateRef  string
		csvPath      string
		mode         string
		outPath      string
		apiKeyEnv    string
		user         string
		pass         string
		skipTLS      bool
		domain       string
		inputYAML    string
	)
	flag.StringVar(&server, "server", "", "Bindplane server URL, e.g. https://bindplane.example.com")
	flag.StringVar(&configName, "config-name", "", "Name of the Bindplane configuration to fetch")
	flag.StringVar(&templateRef, "template-source", "", "Name (or numeric index) of the template windows_event source")
	flag.StringVar(&csvPath, "csv", "", "Path to CSV file (must contain a `hostname` column)")
	flag.StringVar(&mode, "mode", "dry-run", "dry-run | verify | update")
	flag.StringVar(&outPath, "out", "", "Output YAML path for dry-run (default: <config-name>-new.yaml)")
	flag.StringVar(&apiKeyEnv, "api-key-env", "BINDPLANE_API_KEY", "Env var holding the Bindplane API key")
	flag.StringVar(&user, "user", "", "Bindplane basic-auth username (alternative to API key)")
	flag.StringVar(&pass, "pass", "", "Bindplane basic-auth password (prefer --pass-env)")
	var passEnv string
	flag.StringVar(&passEnv, "pass-env", "", "Env var holding the Bindplane basic-auth password (safer than --pass)")
	flag.BoolVar(&skipTLS, "skip-tls-verify", false, "Skip TLS certificate verification (lab only)")
	flag.StringVar(&domain, "windows-domain", "", "Optional Windows domain to set on new sources")
	flag.StringVar(&inputYAML, "input", "", "Load configuration from a local YAML file instead of the API (useful for dry-run)")
	flag.Parse()

	// Resolve --pass-env (takes precedence over --pass).
	if passEnv != "" {
		if v := os.Getenv(passEnv); v != "" {
			pass = v
		}
	}

	if csvPath == "" {
		return fmt.Errorf("--csv is required")
	}
	if templateRef == "" {
		return fmt.Errorf("--template-source is required")
	}
	switch mode {
	case "dry-run", "verify", "update":
	default:
		return fmt.Errorf("--mode must be one of: dry-run, verify, update")
	}
	if (mode == "verify" || mode == "update") && server == "" {
		return fmt.Errorf("--server is required for %s mode", mode)
	}
	if mode == "update" && configName == "" {
		return fmt.Errorf("--config-name is required for update mode")
	}

	// Load configuration as a raw map (to preserve unknown fields on round-trip).
	var cfg map[string]any
	if inputYAML != "" {
		data, err := os.ReadFile(inputYAML)
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parse input yaml: %w", err)
		}
		cfg = normalizeToStringMap(cfg).(map[string]any)
	} else {
		if server == "" || configName == "" {
			return fmt.Errorf("--server and --config-name are required when --input is not set")
		}
		client, err := bpclient.New(bpclient.Options{
			BaseURL:       server,
			APIKey:        os.Getenv(apiKeyEnv),
			User:          user,
			Pass:          pass,
			SkipTLSVerify: skipTLS,
			Timeout:       60 * time.Second,
		})
		if err != nil {
			return err
		}
		body, err := client.GetConfigurationRaw(configName)
		if err != nil {
			return err
		}
		var envelope struct {
			Configuration map[string]any `json:"configuration"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("parse configuration response: %w", err)
		}
		if envelope.Configuration != nil {
			cfg = envelope.Configuration
		} else if err := json.Unmarshal(body, &cfg); err != nil {
			return fmt.Errorf("parse configuration response: %w", err)
		}
	}

	// Windows credentials (always prompted; shared across all new sources).
	username, password, err := promptCreds(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}

	// Parse CSV.
	csvF, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("open csv: %w", err)
	}
	defer csvF.Close()
	rows, err := cloner.ReadCSV(csvF)
	if err != nil {
		return fmt.Errorf("parse csv: %w", err)
	}

	// Clone sources.
	result, err := cloner.Clone(cfg, templateRef, rows, cloner.Creds{
		Username: username,
		Password: password,
		Domain:   domain,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "detected apiVersion=%s, template=%q, added=%d\n",
		result.APIVersion, result.TemplateName, len(result.Added))
	for _, u := range result.UnknownParams {
		fmt.Fprintf(os.Stderr, "warning: CSV column %q does not match any existing parameter on the template source\n", u)
	}

	switch mode {
	case "dry-run":
		return writeDryRun(cfg, outPath, configName, result)
	case "verify":
		return doVerify(server, apiKeyEnv, user, pass, skipTLS, cfg, os.Stdin, os.Stderr)
	case "update":
		return doUpdate(server, apiKeyEnv, user, pass, skipTLS, cfg, configName, os.Stdin, os.Stderr)
	}
	return nil
}

func writeDryRun(cfg map[string]any, outPath, configName string, res *cloner.Result) error {
	if outPath == "" {
		name := configName
		if name == "" {
			name = "config"
		}
		outPath = name + "-new.yaml"
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (added %d sources)\n", outPath, len(res.Added))
	fmt.Fprintln(os.Stderr, "warning: output contains plaintext windows credentials; chmod 600 applied, delete after use.")
	return nil
}

func doVerify(server, apiKeyEnv, user, pass string, skipTLS bool, cfg map[string]any, in io.Reader, out io.Writer) error {
	fmt.Fprint(out, "Name for the verification configuration: ")
	reader := bufio.NewReader(in)
	name, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("verification config name cannot be empty")
	}
	meta, _ := cfg["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		cfg["metadata"] = meta
	}
	meta["name"] = name
	delete(meta, "id")
	delete(meta, "version")

	client, err := bpclient.New(bpclient.Options{
		BaseURL: server, APIKey: os.Getenv(apiKeyEnv),
		User: user, Pass: pass, SkipTLSVerify: skipTLS, Timeout: 60 * time.Second,
	})
	if err != nil {
		return err
	}
	body, err := client.Apply([]map[string]any{cfg})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "applied. response: %s\n", string(body))
	fmt.Fprintf(out, "review in UI: %s/configurations/%s\n", strings.TrimRight(server, "/"), name)
	return nil
}

func doUpdate(server, apiKeyEnv, user, pass string, skipTLS bool, cfg map[string]any, configName string, in io.Reader, out io.Writer) error {
	fmt.Fprintf(out, "About to update configuration %q in place. Type the config name to confirm: ", configName)
	reader := bufio.NewReader(in)
	confirm, _ := reader.ReadString('\n')
	if strings.TrimSpace(confirm) != configName {
		return fmt.Errorf("confirmation mismatch; aborting")
	}
	// Clear stale server-generated fields to avoid version conflicts on apply.
	meta, _ := cfg["metadata"].(map[string]any)
	if meta != nil {
		delete(meta, "id")
		delete(meta, "version")
	}
	client, err := bpclient.New(bpclient.Options{
		BaseURL: server, APIKey: os.Getenv(apiKeyEnv),
		User: user, Pass: pass, SkipTLSVerify: skipTLS, Timeout: 60 * time.Second,
	})
	if err != nil {
		return err
	}
	body, err := client.Apply([]map[string]any{cfg})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "applied. response: %s\n", string(body))
	return nil
}

func promptCreds(in *os.File, out *os.File) (string, string, error) {
	fmt.Fprint(out, "Windows username (shared across all new sources): ")
	reader := bufio.NewReader(in)
	user, err := reader.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return "", "", fmt.Errorf("username cannot be empty")
	}

	fmt.Fprint(out, "Windows password: ")
	var pwd []byte
	if term.IsTerminal(int(in.Fd())) {
		pwd, err = term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
	} else {
		line, rerr := reader.ReadString('\n')
		pwd = []byte(strings.TrimRight(line, "\n"))
		err = rerr
	}
	if err != nil {
		return "", "", err
	}
	if len(pwd) == 0 {
		return "", "", fmt.Errorf("password cannot be empty")
	}
	return user, string(pwd), nil
}

// normalizeToStringMap converts map[interface{}]interface{} from yaml.v3
// (rare but possible) into map[string]any recursively. yaml.v3 generally
// produces string-keyed maps already, but this protects against edge cases.
func normalizeToStringMap(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalizeToStringMap(x)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = normalizeToStringMap(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = normalizeToStringMap(x)
		}
		return t
	default:
		return v
	}
}
