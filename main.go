package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"gotest.tools/gotestsum/cmd"
	"gotest.tools/gotestsum/cmd/tool"
	"gotest.tools/gotestsum/log"
	"gotest.tools/gotestsum/testjson"
)

var version = "master"

func main() {
	err := route(os.Args)
	switch err.(type) {
	case nil:
		return
	case *exec.ExitError:
		// go test should already report the error to stderr, exit with
		// the same status code
		os.Exit(ExitCodeWithDefault(err))
	default:
		log.Error(err.Error())
		os.Exit(3)
	}
}

func route(args []string) error {
	name := args[0]
	next, rest := cmd.Next(args[1:])
	switch next {
	case "tool":
		return tool.Run(name+" "+next, rest)
	default:
		return runMain(name, args[1:])
	}
}

func runMain(name string, args []string) error {
	flags, opts := setupFlags(name)
	switch err := flags.Parse(args); {
	case err == pflag.ErrHelp:
		return nil
	case err != nil:
		flags.Usage()
		return err
	}
	opts.args = flags.Args()
	setupLogging(opts)

	if opts.version {
		fmt.Fprintf(os.Stdout, "gotestsum version %s\n", version)
		return nil
	}
	return run(opts)
}

func setupFlags(name string) (*pflag.FlagSet, *options) {
	opts := &options{
		noSummary:                    newNoSummaryValue(),
		junitTestCaseClassnameFormat: &junitFieldFormatValue{},
		junitTestSuiteNameFormat:     &junitFieldFormatValue{},
	}
	flags := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flags.SetInterspersed(false)
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage:
    %s [flags] [--] [go test flags]

Flags:
`, name)
		flags.PrintDefaults()
		fmt.Fprint(os.Stderr, `
Formats:
    dots                    print a character for each test
    dots-v2                 experimental dots format, one package per line
    pkgname                 print a line for each package
    pkgname-and-test-fails  print a line for each package and failed test output
    testname                print a line for each test and package
    standard-quiet          standard go test format
    standard-verbose        standard go test -v format
`)
	}
	flags.BoolVar(&opts.debug, "debug", false, "enabled debug")
	flags.StringVarP(&opts.format, "format", "f",
		lookEnvWithDefault("GOTESTSUM_FORMAT", "short"),
		"print format of test input")
	flags.BoolVar(&opts.rawCommand, "raw-command", false,
		"don't prepend 'go test -json' to the 'go test' command")
	flags.StringVar(&opts.jsonFile, "jsonfile",
		lookEnvWithDefault("GOTESTSUM_JSONFILE", ""),
		"write all TestEvents to file")
	flags.StringVar(&opts.junitFile, "junitfile",
		lookEnvWithDefault("GOTESTSUM_JUNITFILE", ""),
		"write a JUnit XML file")
	flags.BoolVar(&opts.noColor, "no-color", color.NoColor, "disable color output")
	flags.Var(opts.noSummary, "no-summary",
		"do not print summary of: "+testjson.SummarizeAll.String())
	flags.Var(opts.junitTestSuiteNameFormat, "junitfile-testsuite-name",
		"format the testsuite name field as: "+junitFieldFormatValues)
	flags.Var(opts.junitTestCaseClassnameFormat, "junitfile-testcase-classname",
		"format the testcase classname field as: "+junitFieldFormatValues)
	flags.BoolVar(&opts.version, "version", false, "show version and exit")
	return flags, opts
}

func lookEnvWithDefault(key, defValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defValue
}

type options struct {
	args                         []string
	format                       string
	debug                        bool
	rawCommand                   bool
	jsonFile                     string
	junitFile                    string
	noColor                      bool
	noSummary                    *noSummaryValue
	junitTestSuiteNameFormat     *junitFieldFormatValue
	junitTestCaseClassnameFormat *junitFieldFormatValue
	version                      bool
}

func setupLogging(opts *options) {
	if opts.debug {
		log.SetLevel(log.DebugLevel)
	}
	color.NoColor = opts.noColor
}

func run(opts *options) error {
	ctx := context.Background()
	goTestProc, err := startGoTest(ctx, goTestCmdArgs(opts))
	if err != nil {
		return errors.Wrapf(err, "failed to run %s %s",
			goTestProc.cmd.Path,
			strings.Join(goTestProc.cmd.Args, " "))
	}
	defer goTestProc.cancel()

	out := os.Stdout
	handler, err := newEventHandler(opts, out, os.Stderr)
	if err != nil {
		return err
	}
	defer handler.Close() // nolint: errcheck
	exec, err := testjson.ScanTestOutput(testjson.ScanConfig{
		Stdout:  goTestProc.stdout,
		Stderr:  goTestProc.stderr,
		Handler: handler,
	})
	if err != nil {
		return err
	}
	testjson.PrintSummary(out, exec, opts.noSummary.value)
	if err := writeJUnitFile(opts, exec); err != nil {
		return err
	}
	return goTestProc.cmd.Wait()
}

func goTestCmdArgs(opts *options) []string {
	args := opts.args
	defaultArgs := []string{"go", "test"}
	switch {
	case opts.rawCommand:
		return args
	case len(args) == 0:
		return append(defaultArgs, "-json", pathFromEnv("./..."))
	case !hasJSONArg(args):
		defaultArgs = append(defaultArgs, "-json")
	}
	if testPath := pathFromEnv(""); testPath != "" {
		args = append(args, testPath)
	}
	return append(defaultArgs, args...)
}

func pathFromEnv(defaultPath string) string {
	return lookEnvWithDefault("TEST_DIRECTORY", defaultPath)
}

func hasJSONArg(args []string) bool {
	for _, arg := range args {
		if arg == "-json" || arg == "--json" {
			return true
		}
	}
	return false
}

type proc struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader
	cancel func()
}

func startGoTest(ctx context.Context, args []string) (proc, error) {
	if len(args) == 0 {
		return proc{}, errors.New("missing command to run")
	}

	ctx, cancel := context.WithCancel(ctx)
	p := proc{
		cmd:    exec.CommandContext(ctx, args[0], args[1:]...),
		cancel: cancel,
	}
	log.Debugf("exec: %s", p.cmd.Args)
	var err error
	p.stdout, err = p.cmd.StdoutPipe()
	if err != nil {
		return p, err
	}
	p.stderr, err = p.cmd.StderrPipe()
	if err != nil {
		return p, err
	}
	err = p.cmd.Start()
	if err == nil {
		log.Debugf("go test pid: %d", p.cmd.Process.Pid)
	}
	return p, err
}
