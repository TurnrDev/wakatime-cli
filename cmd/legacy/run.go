package legacy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/wakatime/wakatime-cli/cmd/legacy/configread"
	"github.com/wakatime/wakatime-cli/cmd/legacy/configwrite"
	heartbeatcmd "github.com/wakatime/wakatime-cli/cmd/legacy/heartbeat"
	"github.com/wakatime/wakatime-cli/cmd/legacy/legacyapi"
	"github.com/wakatime/wakatime-cli/cmd/legacy/legacyparams"
	"github.com/wakatime/wakatime-cli/cmd/legacy/logfile"
	"github.com/wakatime/wakatime-cli/cmd/legacy/offlinecount"
	"github.com/wakatime/wakatime-cli/cmd/legacy/offlinesync"
	"github.com/wakatime/wakatime-cli/cmd/legacy/today"
	"github.com/wakatime/wakatime-cli/cmd/legacy/todaygoal"
	"github.com/wakatime/wakatime-cli/pkg/api"
	"github.com/wakatime/wakatime-cli/pkg/config"
	"github.com/wakatime/wakatime-cli/pkg/diagnostic"
	"github.com/wakatime/wakatime-cli/pkg/exitcode"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/log"
	"github.com/wakatime/wakatime-cli/pkg/vipertools"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Run executes legacy commands following the interface of the old python implementation of the WakaTime script.
func Run(cmd *cobra.Command, v *viper.Viper) {
	configFile, err := config.FilePath(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error getting config file path: %s", err)
	}

	if err := config.ReadInConfig(v, configFile); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration file: %s", err)

		SetupLogging(v)
		log.Errorf("failed to load configuration file: %s", err)

		if v.IsSet("entity") {
			RunCmd(v, false, heartbeatcmd.RunWithoutSending)
		}

		os.Exit(exitcode.ErrConfigFileParse)
	}

	logFileParams := SetupLogging(v)

	if v.GetBool("useragent") {
		log.Debugln("command: useragent")

		if plugin := vipertools.GetString(v, "plugin"); plugin != "" {
			fmt.Println(heartbeat.UserAgent(plugin))

			os.Exit(exitcode.Success)
		}

		fmt.Println(heartbeat.UserAgentUnknownPlugin())

		os.Exit(exitcode.Success)
	}

	if v.GetBool("version") {
		log.Debugln("command: version")

		RunCmd(v, logFileParams.Verbose, runVersion)
	}

	if v.IsSet("config-read") {
		log.Debugln("command: config-read")

		RunCmd(v, logFileParams.Verbose, configread.Run)
	}

	if v.IsSet("config-write") {
		log.Debugln("command: config-write")

		RunCmd(v, logFileParams.Verbose, configwrite.Run)
	}

	if v.GetBool("today") {
		log.Debugln("command: today")

		RunCmd(v, logFileParams.Verbose, today.Run)
	}

	if v.IsSet("today-goal") {
		log.Debugln("command: today-goal")

		RunCmd(v, logFileParams.Verbose, todaygoal.Run)
	}

	if v.IsSet("entity") {
		log.Debugln("command: heartbeat")

		RunCmdWithOfflineSync(v, logFileParams.Verbose, heartbeatcmd.Run)
	}

	if v.IsSet("sync-offline-activity") {
		log.Debugln("command: sync-offline-activity")

		RunCmd(v, logFileParams.Verbose, offlinesync.Run)
	}

	if v.GetBool("offline-count") {
		log.Debugln("command: offline-count")

		RunCmd(v, logFileParams.Verbose, offlinecount.Run)
	}

	log.Warnf("one of the following parameters has to be provided: %s", strings.Join([]string{
		"--config-read",
		"--config-write",
		"--entity",
		"--offline-count",
		"--sync-offline-activity",
		"--today",
		"--today-goal",
		"--useragent",
		"--version",
	}, ", "))

	_ = cmd.Help()

	os.Exit(exitcode.ErrGeneric)
}

// SetupLogging uses the --log-file param to configure logging to file or stdout.
func SetupLogging(v *viper.Viper) *logfile.Params {
	logfileParams, err := logfile.LoadParams(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load log params: %s", err)
		log.Fatalf("failed to load log params: %s", err)
	}

	logFile := os.Stdout

	if !logfileParams.ToStdout {
		logFile, err = os.OpenFile(logfileParams.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening log file: %s", err)
			log.Fatalf("error opening log file: %s", err)
		}

		log.SetOutput(logFile)
	}

	log.SetVerbose(logfileParams.Verbose)
	log.SetJww(logfileParams.Verbose, logFile)

	return &logfileParams
}

// cmdFn represents a command function.
type cmdFn func(v *viper.Viper) (int, error)

// RunCmd runs a command function and exits with the exit code returned by
// the command function. Will send diagnostic on any errors or panics.
func RunCmd(v *viper.Viper, verbose bool, cmd cmdFn) {
	exitCode := runCmd(v, verbose, cmd)

	os.Exit(exitCode)
}

// RunCmdWithOfflineSync runs a command function and exits with the exit code
// returned by the command function. If command run was successful, it will execute
// offline sync command afterwards. Will send diagnostic on any errors or panics.
func RunCmdWithOfflineSync(v *viper.Viper, verbose bool, cmd cmdFn) {
	exitCode := runCmd(v, verbose, cmd)
	if exitCode != exitcode.Success {
		os.Exit(exitCode)
	}

	os.Exit(runCmd(v, verbose, offlinesync.Run))
}

// runCmd contains the main logic of RunCmd.
func runCmd(v *viper.Viper, verbose bool, cmd cmdFn) int {
	logs := bytes.NewBuffer(nil)
	resetLogs := captureLogs(logs)

	// catch panics
	defer func() {
		if err := recover(); err != nil {
			resetLogs()

			if !verbose {
				sendDiagnostics(v, logs.String(), string(debug.Stack()))
			}

			os.Exit(exitcode.ErrGeneric)
		}
	}()

	// run command
	exitCode, err := cmd(v)
	if err != nil {
		log.Errorf("failed to run command: %s", err.Error())

		resetLogs()

		if exitCode != exitcode.ErrAuth && verbose {
			sendDiagnostics(v, logs.String(), string(debug.Stack()))
		}
	}

	return exitCode
}

func sendDiagnostics(v *viper.Viper, logs, stack string) {
	params, err := legacyparams.Load(v, true)
	if err != nil {
		log.Errorf("failed to load parameters for sending diagnostics: %s", err)

		return
	}

	c, err := legacyapi.NewClientWithoutAuth(params.API)
	if err != nil {
		log.Errorf("failed to initialize api client for sending diagnostics: %s", err)

		return
	}

	diagnostics := []diagnostic.Diagnostic{
		diagnostic.Logs(logs),
		diagnostic.Stack(stack),
	}

	api.WithDisableSSLVerify()(c)

	err = c.SendDiagnostics(params.API.Plugin, diagnostics...)
	if err != nil {
		log.Errorf("failed to send diagnostics: %s", err)

		return
	}

	log.Debugln("successfully sent diagnostics")
}

func captureLogs(dest io.Writer) func() {
	logOutput := log.Output()

	// will write to log output and dest
	mw := io.MultiWriter(logOutput, dest)

	log.SetOutput(mw)

	return func() {
		log.SetOutput(logOutput)
	}
}
