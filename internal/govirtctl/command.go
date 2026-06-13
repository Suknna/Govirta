package govirtctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suknna/govirta/internal/version"
)

// objectEnvelope is the minimal projection the CLI decodes from a manifest to
// route it: the kind and name are the operator's authoritative choice, never
// inferred. Everything else in the manifest is forwarded verbatim to the master.
type objectEnvelope struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name            string `json:"name"`
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
}

// Run is the govirtctl entrypoint. args excludes the program name. It writes
// command output to stdout and diagnostics to stderr, returning a process exit
// code (0 success, non-zero failure).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	switch args[0] {
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "replace":
		return runReplace(ctx, args[1:], stdout, stderr)
	case "get":
		return runGet(ctx, args[1:], stdout, stderr)
	case "delete":
		return runDelete(ctx, args[1:], stdout, stderr)
	case "image":
		return runImage(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, versionString())
		return 0
	default:
		fmt.Fprintf(stderr, "govirtctl: unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage:
  govirtctl apply --server <url> -f <manifest.json>
	govirtctl replace --server <url> -f <manifest.json>
	govirtctl get --server <url> <kind> <name>
	govirtctl delete --server <url> <kind> <name>
	govirtctl image upload --server <url> --name <name> --uid <uid> --version <version> --format <qcow2|raw|iso> --file <path>
	govirtctl version`

// runApply reads a manifest file, locates its kind/name, and applies it.
func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root, e.g. http://127.0.0.1:8080 (required)")
	file := fs.String("f", "", "path to the resource manifest JSON file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl apply: --server is required")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "govirtctl apply: -f <manifest> is required")
		return 2
	}

	body, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: read manifest %q: %v\n", *file, err)
		return 1
	}

	var env objectEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: decode manifest %q: %v\n", *file, err)
		return 1
	}
	if env.Kind == "" {
		fmt.Fprintf(stderr, "govirtctl apply: manifest %q has no kind\n", *file)
		return 1
	}
	if env.Metadata.Name == "" {
		fmt.Fprintf(stderr, "govirtctl apply: manifest %q has no metadata.name\n", *file)
		return 1
	}

	c := NewClient(*server, nil)
	if _, err := c.Apply(ctx, env.Kind, env.Metadata.Name, body); err != nil {
		fmt.Fprintf(stderr, "govirtctl apply: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s/%s applied\n", env.Kind, env.Metadata.Name)
	return 0
}

// runReplace reads a manifest file and sends it through the master's guarded
// replace path. The manifest must carry metadata.resourceVersion from a prior
// get/edit workflow so the master can reject stale writes.
func runReplace(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root, e.g. http://127.0.0.1:8080 (required)")
	file := fs.String("f", "", "path to the resource manifest JSON file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl replace: --server is required")
		return 2
	}
	if *file == "" {
		fmt.Fprintln(stderr, "govirtctl replace: -f <manifest> is required")
		return 2
	}

	body, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl replace: read manifest %q: %v\n", *file, err)
		return 1
	}

	var env objectEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		fmt.Fprintf(stderr, "govirtctl replace: decode manifest %q: %v\n", *file, err)
		return 1
	}
	if env.Kind == "" {
		fmt.Fprintf(stderr, "govirtctl replace: manifest %q has no kind\n", *file)
		return 1
	}
	if env.Metadata.Name == "" {
		fmt.Fprintf(stderr, "govirtctl replace: manifest %q has no metadata.name\n", *file)
		return 1
	}
	if env.Metadata.ResourceVersion == "" {
		fmt.Fprintf(stderr, "govirtctl replace: manifest %q has no metadata.resourceVersion; run govirtctl get first\n", *file)
		return 2
	}

	c := NewClient(*server, nil)
	if _, err := c.Replace(ctx, env.Kind, env.Metadata.Name, body); err != nil {
		fmt.Fprintf(stderr, "govirtctl replace: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s/%s replaced\n", env.Kind, env.Metadata.Name)
	return 0
}

// runGet fetches one object and prints the JSON object exactly as returned by
// the apiserver. The output is intentionally machine-editable: operators can
// redirect it to a file, edit spec fields, and pass it to `govirtctl replace`.
func runGet(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl get: --server is required")
		return 2
	}
	if len(rest) != 2 {
		fmt.Fprintln(stderr, "govirtctl get: expected <kind> <name>")
		return 2
	}
	kind, name := rest[0], rest[1]

	c := NewClient(*server, nil)
	body, _, err := c.Get(ctx, kind, name)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl get: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(body, '\n')); err != nil {
		fmt.Fprintf(stderr, "govirtctl get: write output: %v\n", err)
		return 1
	}

	return 0
}

// runDelete triggers the master's finalizer-two-phase deletion for one object.
// On acceptance it prints "<kind>/<name> deleting" (async teardown is still in
// flight, not done). A 409 surfaces the apiserver's "still referenced by ..."
// protection text so the operator sees what blocks the delete.
func runDelete(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if *server == "" {
		fmt.Fprintln(stderr, "govirtctl delete: --server is required")
		return 2
	}
	if len(rest) != 2 {
		fmt.Fprintln(stderr, "govirtctl delete: expected <kind> <name>")
		return 2
	}
	kind, name := rest[0], rest[1]

	c := NewClient(*server, nil)
	if err := c.Delete(ctx, kind, name); err != nil {
		fmt.Fprintf(stderr, "govirtctl delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s/%s deleting\n", kind, name)
	return 0
}

func runImage(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "govirtctl image: expected subcommand upload")
		return 2
	}
	switch args[0] {
	case "upload":
		return runImageUpload(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "govirtctl image: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runImageUpload(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("image upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "master apiserver root (required)")
	name := fs.String("name", "", "Image metadata.name (required)")
	uid := fs.String("uid", "", "Image metadata.uid (required)")
	version := fs.String("version", "", "Image content version (required)")
	format := fs.String("format", "", "Image byte format: qcow2, raw, or iso (required)")
	file := fs.String("file", "", "path to image bytes (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server == "" || *name == "" || *uid == "" || *version == "" || *format == "" || *file == "" {
		fmt.Fprintln(stderr, "govirtctl image upload: --server, --name, --uid, --version, --format, and --file are required")
		return 2
	}
	size, digest, err := fileSizeAndSHA256(*file)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl image upload: inspect file %q: %v\n", *file, err)
		return 1
	}
	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintf(stderr, "govirtctl image upload: open file %q: %v\n", *file, err)
		return 1
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(stderr, "govirtctl image upload: close file %q: %v\n", *file, err)
		}
	}()

	c := NewClient(*server, nil)
	if _, err := c.UploadImage(ctx, *name, *uid, *version, *format, size, digest, f); err != nil {
		fmt.Fprintf(stderr, "govirtctl image upload: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Image/%s version %s uploaded\n", *name, *version)
	return 0
}

func fileSizeAndSHA256(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(h.Sum(nil)), nil
}

// versionString returns the Govirta version line for the `version` subcommand.
// It mirrors the prior version-only behaviour of cmd/govirtctl.
func versionString() string {
	return version.String()
}
