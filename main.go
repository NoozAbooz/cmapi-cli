package main

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/google/shlex"
	cp "github.com/otiai10/copy"
)

// Contains checks if a string is in a string array
// No side effect
func Contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

// ReadJson reads a json file and returns a string map.
// No side effect
func ReadJson(filename string) map[string]string {
	// Read the secret from the file
	file, err := os.Open(filename)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Read the file
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}

	// Parse the JSON
	var secret map[string]string
	err = json.Unmarshal([]byte(data), &secret)
	if err != nil {
		return nil
	}

	return secret
}

// WriteJson takes a string map and writes it to a json file.
// No side effect
func WriteJson(filename string, input map[string]string) bool {
	// Marshal the map
	data, err := json.MarshalIndent(input, "", "    ")
	if err != nil {
		return false
	}

	// Write the file
	err = os.WriteFile(filename, data, 0600)
	return err == nil
}

// SetupSecret read the secret from the file and update it if needed
func SetupSecret() bool {
	SecretFilePath = filepath.Join(AdminDir, ".cmapi-cli-secret.json")

	if _, err := os.Stat(SecretFilePath); os.IsNotExist(err) {
		user, _ := user.Current()
		SetDefaultSecret(user, Secret)
		WriteJson(SecretFilePath, Secret)
	}

	secretFromFile := ReadJson(SecretFilePath)
	if secretFromFile == nil {
		return Fail(126)
	}

	if UpdateFileSecret(Secret, secretFromFile) {
		WriteJson(SecretFilePath, secretFromFile)
		fmt.Println(Yellow("Secret file updated."))
	}
	Secret = secretFromFile

	return true
}

// SetDefaultSecret sets the default secret based on the current user.
// No side effect
func SetDefaultSecret(user *user.User, secret map[string]string) {
	name := strings.TrimSpace(user.Username)
	if name != "" {
		secret["computer-name"] = name
	}

	secret["workspace-dir"] = filepath.Join(user.HomeDir, "cmapi-projects")
}

// UpdateFileSecret updates missing keys in the file.
// No side effect
func UpdateFileSecret(secret map[string]string, fileSecret map[string]string) bool {
	outdated := false
	for key, value := range secret {
		if _, ok := fileSecret[key]; !ok {
			fileSecret[key] = value
			outdated = true
		}
	}

	return outdated
}

// RunCommand runs a command and returns the exit code.
func RunCommand(cmd *exec.Cmd) int {
	RunningCommands.PushBack(cmd)

	defer func() {
		for e := RunningCommands.Front(); e != nil; e = e.Next() {
			if e.Value == cmd {
				RunningCommands.Remove(e)
				break
			}
		}
	}()

	err := cmd.Run()
	if err != nil {
		if exit_err, ok := err.(*exec.ExitError); ok {
			return exit_err.ExitCode()
		} else {
			return -1
		}
	}
	return 0
}

// RunCommand runs a command and returns the output, error and exit code.
// The output is printed to stdout and stderr
// Color code might not work on Windows
// https://stackoverflow.com/questions/1312922/detect-if-stdin-is-a-terminal-or-pipe
// https://stackoverflow.com/questions/1401002/how-to-trick-an-application-into-thinking-its-stdout-is-a-terminal-not-a-pipe
func RunCommandPrintOut(workingDir string, name string, arg ...string) (string, string, int) {
	cmd := ExecCommand(name, arg...)
	cmd.Dir = workingDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	exitCode := RunCommand(cmd)
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	return stdout, stderr, exitCode
}

// RunCommandGetOutput runs a command and returns the output, error and exit code
// The output is not printed
func RunCommandGetOutput(workingDir string, name string, arg ...string) (string, string, int) {
	cmd := ExecCommand(name, arg...)
	cmd.Dir = workingDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	exitCode := RunCommand(cmd)
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	return stdout, stderr, exitCode
}

// RunCommandGetStatus runs a command and returns the exit code
// The output is printed
func RunCommandGetStatus(workingDir string, name string, arg ...string) int {
	cmd := ExecCommand(name, arg...)
	cmd.Dir = workingDir

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Some commands like Git will mess up the terminal color on Windows
	defer FixConsoleColor()

	return RunCommand(cmd)
}

// IsCommandSuccess runs a command and returns true if the exit code is 0
// The output is not printed
func IsCommandSuccess(workingDir string, name string, arg ...string) bool {
	return RunCommandGetStatus(workingDir, name, arg...) == 0
}

// SetupEnvironment checks if the environment is set up correctly.
func SetupEnvironment() bool {
	success := true

	usr, err := user.Current()
	if err != nil {
		return Fail(127) // critical failure
	}

	if runtime.GOOS == "linux" {
		ids, err := usr.GroupIds()
		if err != nil {
			return Fail(127) // critical failure
		}

		dialoutGroup, err := user.LookupGroup("dialout")
		// if the group exists and the user is not in it
		if err == nil && !Contains(ids, dialoutGroup.Gid) {
			success = Fail(133)
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		success = Fail(100)
	}
	if _, err := exec.LookPath("pros"); err != nil {
		success = Fail(101)
	}

	if _, ok := os.LookupEnv("PROS_TOOLCHAIN"); !ok {
		success = Fail(132)
	}

	AdminDir = filepath.Join(usr.HomeDir, ".cmapi-cli")
	if os.MkdirAll(AdminDir, os.ModePerm) != nil {
		success = Fail(128)
	}

	wd, err := os.Getwd()
	if err != nil {
		success = Fail(129)
	}
	WorkingDir = wd

	return success // return true if and only if all checks passed
}

// Returns true if the given path is a Git repository.
func IsGitRepo(projectRoot string) bool {
	return IsCommandSuccess(projectRoot, "git", "rev-parse")
}

// Returns true if the given path is a PROS project. However, it does not check if the project is set up probably.
// No side effect
func IsProsProject(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, "project.pros"))
	return !os.IsNotExist(err)
}

// GetRepoUrl returns the repo url of the given repository.
func GetRepoUrl(repoSlug string) string {
	return "https://" + Secret["username"] + ":" + Secret["password"] + "@bitbucket.org/" +
		Secret["workspace"] + "/" + repoSlug + ".git"
}

func LinkLocalRepoToServerCommand(projectRoot string, repoSlug string) bool {
	if !IsGitRepo(projectRoot) {
		return Fail(102)
	}

	url := GetRepoUrl(repoSlug)
	setUrl, _, code := RunCommandGetOutput(projectRoot, "git", "remote", "get-url", "origin")

	var result bool
	if strings.TrimSpace(setUrl) == url {
		result = true
	} else if code == 0 {
		result = IsCommandSuccess(projectRoot, "git", "remote", "set-url", "origin", url)
	} else {
		result = IsCommandSuccess(projectRoot, "git", "remote", "add", "origin", url)
	}

	if !result {
		return Fail(103)
	}

	if !IsCommandSuccess(projectRoot, "git", "config", "user.name", Secret["computer-name"]) ||
		!IsCommandSuccess(projectRoot, "git", "config", "user.email", Secret["email"]) ||
		!IsCommandSuccess(projectRoot, "git", "config", "commit.gpgsign", "false") {
		return Fail(104)
	}

	return Success("Linked '%s' -> 'https://bitbucket.org/%s'.", projectRoot, Secret["workspace"]+"/"+repoSlug)
}

// BackupCommand backs up the project to the server
func BackupCommand(projectRoot string) bool {
	if !IsGitRepo(projectRoot) {
		return Fail(102)
	}

	if !IsCommandSuccess(projectRoot, "git", "add", "-A") ||
		!IsCommandSuccess(projectRoot, "git", "commit", "-m", "Backup") {
		return Fail(105)
	}

	if !IsCommandSuccess(projectRoot, "git", "push", "-u", "origin", "master") {
		return Fail(106)
	}

	return Success("All changes have been backed up to the server.")
}

func BuildCommand(projectRoot string) bool {
	if !IsProsProject(projectRoot) {
		return Fail(134)
	}

	fmt.Println(Yellow("------------------ Make Project ------------------"))

	var result bool
	result = IsCommandSuccess(projectRoot, "make", "-j")

	if !result {
		BeepFail()
		return Fail(107)
	}

	BeepSuccess()
	return true
}

func CompileCommand(projectRoot string, all bool, slot int) bool {
	if !IsProsProject(projectRoot) {
		return Fail(134)
	}

	fmt.Println(Yellow("------------------ Make Project ------------------"))

	var result bool
	if all {
		result = IsCommandSuccess(projectRoot, "make", "all", "-j")
	} else {
		result = IsCommandSuccess(projectRoot, "make", "-j")
	}

	if !result {
		BeepFail()
		return Fail(107)
	}

	for {
		info, _, _ := RunCommandGetOutput(projectRoot, "pros", "lsusb", "--target", "v5")
		if strings.Contains(info, " - ") {
			break
		}
		fmt.Println(Yellow("V5 product not found, retrying..."))
	}

	fmt.Println(Yellow("Starting to upload"))

	for i := 0; i < 6; i++ {
		if i != 0 {
			fmt.Printf(Yellow("Upload failed, retrying... (%d/5)\n"), i)
		}
		if IsCommandSuccess(projectRoot, "pros", "upload", "--after", "screen", "--slot", strconv.Itoa(slot)) {
			BeepSuccess()
			return true
		}
	}

	BeepFail()
	return Fail(108)
}

func InitProjectCommand(projectRoot string, kernel string, force bool, noPull bool) bool {
	if IsProsProject(projectRoot) && !force {
		return Fail(109)
	}

	if !InitGitRepo(projectRoot) {
		return false
	}

	if !IsCommandSuccess(projectRoot, "git", "config", "commit.gpgsign", "false") {
		return Fail(104)
	}

	if !IsCommandSuccess(projectRoot, "git", "add", "-A") ||
		!IsCommandSuccess(projectRoot, "git", "commit", "--allow-empty", "-m", "Apply PROS kernel") {
		return Fail(110)
	}

	result := InitProsProjectAndApplyKernel(projectRoot, kernel, noPull)

	// No matter what, we reset the repository
	// Apply the kernel to the project without overwriting any existing files.
	if !IsCommandSuccess(projectRoot, "git", "reset", "--hard") {
		return Fail(111)
	}

	return result && Success("Initialized PROS project at '%s'.", projectRoot)
}

func PullCommand(projectRoot string) bool {
	if !IsGitRepo(projectRoot) {
		return Fail(102)
	}

	if !IsCommandSuccess(projectRoot, "git", "pull") {
		return Fail(112)
	}

	return Success("All changes have been pulled from the server.")
}

func CloneRepositoryCommand(label string, workspaceDir string, kernel string, noPull bool) bool {
	projectRootName := Secret["repo-slug-prefix"] + label
	repoSlug := strings.ToLower(projectRootName)
	projectRoot := filepath.Join(workspaceDir, projectRootName)

	err := os.MkdirAll(projectRoot, os.ModePerm)
	if err != nil {
		return Fail(113, projectRoot)
	}

	url := GetRepoUrl(repoSlug)

	if !IsCommandSuccess(workspaceDir, "git", "clone", url, projectRootName) {
		return Fail(114)
	}

	if !InitProsProjectAndApplyKernel(projectRoot, kernel, noPull) {
		return false
	}

	// No matter what, we reset the repository
	// Apply the kernel to the project without overwriting any existing files.
	if !IsCommandSuccess(projectRoot, "git", "reset", "--hard") {
		return Fail(131)
	}

	return Success("Cloned 'https://bitbucket.org/%s' -> '%s'.", Secret["workspace"]+"/"+repoSlug, projectRoot)
}

func CreateRepositoryCommand(label string, workspaceDir string, kernel string, noPull bool, isLocal bool) bool {
	templateRepoSlug := strings.ToLower(Secret["template-repo"])
	templateRoot := filepath.Join(AdminDir, templateRepoSlug)

	if !noPull {
		if !IsGitRepo(templateRoot) {
			if !IsCommandSuccess(AdminDir, "git", "clone", GetRepoUrl(templateRepoSlug), templateRepoSlug) {
				return Fail(115)
			}
		} else {
			if !LinkLocalRepoToServerCommand(templateRoot, templateRepoSlug) {
				return Fail(116)
			}

			// Do not use PullCommand because the default branch may not be master
			if _, _, code := RunCommandPrintOut(templateRoot, "git", "pull"); code != 0 {
				return Fail(112)
			}
		}
	}

	if !IsGitRepo(templateRoot) {
		return Fail(117, templateRoot)
	}

	projectRootName := Secret["repo-slug-prefix"] + label
	projectSlug := strings.ToLower(projectRootName)
	projectRoot := filepath.Join(workspaceDir, projectRootName)

	if IsGitRepo(projectRoot) {
		return Fail(118)
	}

	err := os.MkdirAll(projectRoot, os.ModePerm)
	if err != nil {
		return Fail(113, projectRoot)
	}

	// copy everything from the template to the project
	opt := cp.Options{
		Skip: func(info os.FileInfo, src, dest string) (bool, error) {
			return strings.HasSuffix(src, ".git"), nil
		},
	}
	if cp.Copy(templateRoot, projectRoot, opt) != nil {
		return Fail(119)
	}

	if !InitProjectCommand(projectRoot, kernel, true, noPull) {
		return false
	}

	if !isLocal {
		status, err := CreateRemoteRepo(label)
		if err != nil {
			return Fail(120)
		}

		if status != "200 OK" {
			return Fail(121, status)
		}

		if !LinkLocalRepoToServerCommand(projectRoot, projectSlug) {
			return false
		}

		if !IsCommandSuccess(projectRoot, "git", "push", "-u", "origin", "master") {
			return Fail(122)
		}
	}

	return Success("Created repository at '%s'.", projectRoot)
}

func ListSecretsCommand() bool {
	fmt.Println(Yellow("Listing secrets..."))

	for key, value := range Secret {
		fmt.Println(Yellow(key+": ") + value)
	}

	return true
}

func SetSecretCommand(key string, value string) bool {
	if _, ok := Secret[key]; !ok {
		return Fail(130, key)
	} else {
		Secret[key] = value
		WriteJson(SecretFilePath, Secret)
		return true
	}
}

// InitGitRepo initializes a git repository in the project directory
func InitGitRepo(projectRoot string) bool {
	if !IsGitRepo(projectRoot) {
		if !IsCommandSuccess(projectRoot, "git", "init", "--initial-branch=master") {
			return Fail(123)
		}
	}

	return true
}

// InitProsProjectAndApplyKernel initializes a PROS project in the current directory
func InitProsProjectAndApplyKernel(projectRoot string, kernel string, noPull bool) bool {
	projectName := filepath.Base(projectRoot)

	contents := `{
	"py/object": "pros.conductor.project.Project",
	"py/state": {"project_name": "` + projectName + `", "target": "v5", "templates": {}, "upload_options": {}}
}`
	err := os.WriteFile(filepath.Join(projectRoot, "project.pros"), []byte(contents), 0644)
	if err != nil {
		return Fail(124)
	}

	args := []string{"conductor", "install", "kernel@" + kernel, "-force-system"}
	if noPull {
		args = append(args, "--no-download")
	}

	out, errs, code := RunCommandPrintOut(projectRoot, "pros", args...)
	if strings.Contains(out, "ERROR") || strings.Contains(errs, "ERROR") || code != 0 {
		return Fail(125)
	}

	return true
}

// CreateRemoteRepo creates a remote repo on the BitBucket server, requires label with no spaces
func CreateRemoteRepo(label string) (string, error) {
	projectRootName := Secret["repo-slug-prefix"] + label
	repoSlug := strings.ToLower(projectRootName)

	url := "https://api.bitbucket.org/2.0/repositories/" + Secret["workspace"] + "/" + repoSlug
	method := "POST"
	payload := strings.NewReader(`{
        "scm": "git",
        "project": {
            "key": "` + Secret["project"] + `"
        },
        "name": "` + Secret["repo-name-prefix"] + label + `",
        "language": "c++",
        "is_private": true
    }`)

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		return "", err
	}

	auth := base64.StdEncoding.EncodeToString([]byte(Secret["username"] + ":" + Secret["password"]))
	req.Header.Add("Authorization", "Basic "+auth)
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	_, err = io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return res.Status, err
}

// HandleCommand handles the command line arguments
// Returns true if the command was executed, false otherwise
func HandleCommand(command string, args []string) bool {
	fs := flag.NewFlagSet("options", flag.ContinueOnError)
	var workspaceDir string
	var forceFlag bool
	var kernelVer string
	var localFlag bool
	var noPullFlag bool
	var slotFlag int
	fs.StringVar(&workspaceDir, "d", Secret["workspace-dir"], "")
	fs.StringVar(&workspaceDir, "directory", Secret["workspace-dir"], "")
	fs.BoolVar(&forceFlag, "f", false, "")
	fs.BoolVar(&forceFlag, "force", false, "")
	fs.StringVar(&kernelVer, "k", "latest", "")
	fs.StringVar(&kernelVer, "kernel", "latest", "")
	fs.BoolVar(&localFlag, "l", false, "")
	fs.BoolVar(&localFlag, "local", false, "")
	fs.BoolVar(&noPullFlag, "np", false, "")
	fs.BoolVar(&noPullFlag, "no-pull", false, "")
	fs.IntVar(&slotFlag, "s", 1, "")
	fs.IntVar(&slotFlag, "slot", 1, "")

	fs.Parse(args)

	if command == "all" {
		CompileCommand(WorkingDir, true, slotFlag)
	} else if command == "backup" {
		BackupCommand(WorkingDir)
	} else if command == "init" {
		InitProjectCommand(WorkingDir, kernelVer, forceFlag, noPullFlag)
	} else if command == "link" {
		repoSlug := filepath.Base(WorkingDir)
		if len(fs.Args()) > 0 {
			repoSlug = fs.Arg(0)
		}
		LinkLocalRepoToServerCommand(WorkingDir, repoSlug)
	} else if command == "b" {
		BuildCommand(WorkingDir)
	} else if command == "normal" {
		CompileCommand(WorkingDir, false, slotFlag)
	} else if command == "pull" {
		PullCommand(WorkingDir)
	} else if command == "clone" {
		label := fs.Arg(0)
		if !IsValidLabel(label) {
			return Fail(200)
		}
		CloneRepositoryCommand(label, workspaceDir, kernelVer, noPullFlag)
	} else if command == "create" {
		label := fs.Arg(0)
		if !IsValidLabel(label) {
			return Fail(200)
		}
		CreateRepositoryCommand(label, workspaceDir, kernelVer, noPullFlag, localFlag)
	} else if command == "help" {
		fmt.Println(Yellow(usage))
	} else if command == "secret" {
		if len(fs.Args()) != 2 {
			ListSecretsCommand()
		} else {
			SetSecretCommand(fs.Arg(0), fs.Arg(1))
		}
	} else {
		return Fail(301, command)
	}

	// TODO

	return true
}

func BeepFail() {
	Beep(1175, 100)
}

func BeepSuccess() {
	Beep(1568, 100)
}

// Returns yellow text.
// No side effect
func Yellow(s string) string {
	YELLOW := "\033[93m"
	RESET := "\033[0m"
	return YELLOW + s + RESET
}

// Print an error message in yellow
func Fail(code int, args ...any) bool {
	fmt.Printf(Yellow("Error %d: %s\n"), code, fmt.Sprintf(ErrorCode[code], args...))
	return false
}

func Success(str string, args ...any) bool {
	fmt.Printf(Yellow("%s\n"), fmt.Sprintf(str, args...))
	return true
}

var ErrorCode = map[int]string{
	100: "Git is not installed or not in the PATH.",
	101: "PROS is not installed or not in the PATH.",
	102: "Not a git repository.",
	103: "Failed to link remote repository.",
	104: "Failed to set git config.",
	105: "Failed to create the backup commit.",
	106: "Failed to push the backup commit.",
	107: "Failed to make.",
	108: "Failed to upload.",
	109: "PROS project already exists. Use --force to overwrite the project.pros file.",
	110: "Failed to create the initial commit.",
	111: "Failed to reset to the initial commit.",
	112: "Failed to pull.",
	113: "Failed to create the project directory '%s'.",
	114: "Failed to clone.",
	115: "Failed to clone the template repository.",
	116: "Failed to link the template repository.",
	117: "No template repository found in the local machine at '%s'.",
	118: "Repository already exists.",
	119: "Failed to copy the template repository.",
	120: "Failed to create the remote repository.",
	121: "Failed to create the remote repository with status %s.",
	122: "Failed to push to the server.",
	123: "Failed to initialize git repository.",
	124: "Failed to write project.pros",
	125: "Failed to install kernel.",
	126: "Failed to read the secret file.",
	127: "Failed to get user information.",
	128: "Failed to access the administrator directory.",
	129: "Failed to get working directory.",
	130: "Secret key '%s' does not exist.",
	131: "Failed to reset to the latest commit.",
	132: "'PROS_TOOLCHAIN' environment variable is not defined.",
	133: "User should be in the 'dialout' group.",
	134: "Not a PROS project, use command 'init' to initialize it.",
	200: "Invalid label, only capital letters, digits and hyphens are accepted.",
	300: "Failed to parse command line.",
	301: "Unknown command '%s'.",
}

const usage = `Usage: <command> [<args>, ...]

Commands for project action:
    all [--slot]
        Remove all object files in the project's ./bin directory and compile 
        all source files again. Attempt to connect the V5 Brain and upload the 
        binary files.
    backup
        Commit and push all changes in the repository to the remote server.
    init [--kernel <VERSION>] [--no-pull] [--force]
        Initialize the Git repository and create the PROS project. Apply the 
        kernel to the project without overwriting any existing files.
    link [PROJECT_SLUG]
        Link the current directory to a remote repository on Bitbucket. The 
        project slug is the same as the project root directory name by default.
    normal [--slot]
        Compile source files normally in the current PROS project. Attempt to 
        connect the V5 Brain and upload the binary files.
    pull
        Pull changes from the remote server to the local repository.

Commands for repository management:
    clone [--directory <PATH>] [--kernel <VERSION>] [--no-pull] <LABEL>
        1. Clone a repository from the server to the local machine.
        2. Initialize the PROS project.
    create [--directory <PATH>] [--kernel <VERSION>] [--no-pull] [--local]
        <LABEL>
        1. Create a repository on the local machine. The label should be all
        caps and contain no spaces.
        2. Fork all contents from the template repository.
        3. Initialize the PROS project.
        4. Upload the repository to the server.
    help
        Display this help message.
    secret [<KEY> <VALUE>]
        List all secret keys and values or set a secret key and value.

Options:
    -d,  --directory <PATH>     The workspace directory. The parent directory
                                of where all repositories located at.
                                [default: DEFAULT SETTING]
    -f,  --force                Force the action to run.
    -k,  --kernel <VERSION>     The kernel version to use. [default: latest]
    -l,  --local                Do not create a repository on the server.
    -np, --no-pull              Do not pull template changes/kernel online.
    -s,  --slot <SLOT>          Upload the binary to a specified program slot
                                in the brain. [default: 1, range: 1-8]

Version: 0.1.8`

// Returns true if the given label is valid
// No side effect
var (
	ExecCommand  = exec.Command
	IsValidLabel = regexp.MustCompile(`^[A-Z0-9\-]+$`).MatchString
)

var (
	AdminDir       string
	WorkingDir     string
	SecretFilePath string
	Secret         = map[string]string{
		"computer-name":    "unknown",
		"email":            "cmass-robotics-team-bot@proton.me",
		"username":         "cmass-robotics-team-bot",
		"password":         "",
		"workspace":        "vex7984",
		"workspace-dir":    "",
		"project":          "CURRENT",
		"template-repo":    "cmapi-build",
		"repo-slug-prefix": "7984-",
		"repo-name-prefix": "7984 - ",
	}
	RunningCommands = list.New()

	// Codes from github.com/gen2brain/beeep
	// ErrUnsupported is returned when operating system is not supported.
	ErrUnsupported = errors.New("beeep: unsupported operating system: " + runtime.GOOS)
	ErrNoData      = errors.New("no data")
)

func main() {
	FixConsoleColor()
	BeepSuccess()

	var force bool
	fs := flag.NewFlagSet("Startup", flag.ContinueOnError)
	fs.BoolVar(&force, "f", false, "")
	fs.BoolVar(&force, "force", false, "")
	fs.Parse(os.Args[1:])

	if !SetupEnvironment() && !force {
		return
	}

	if !SetupSecret() && !force {
		return
	}

	fmt.Println(Yellow("Press enter to execute 'normal' command or previous command again (if any)."))
	fmt.Println(Yellow("Use 'help' to see all commands."))

	lastCommandLine := "normal"
	reader := NonBlockingReader{}
	reader.New()
	reader.errFunc = func(err error) {
		for e := RunningCommands.Front(); e != nil; e = e.Next() {
			e.Value.(*exec.Cmd).Process.Kill()
		}
		os.Exit(0)
	}
	for {
		for {
			if _, err := reader.NonBlockingRead(); err == ErrNoData {
				break
			}
		}
		fmt.Print(Yellow("\n> "))
		rawText, _ := reader.BlockingRead()

		if len(rawText) == 0 { // EOF
			break
		}

		commandLine := strings.TrimSpace(rawText)
		if commandLine == "" {
			commandLine = lastCommandLine
			fmt.Println(Yellow("Execute last command: " + commandLine))
		}

		input, err := shlex.Split(commandLine)
		if err != nil {
			Fail(300)
			continue
		}

		if len(input) == 0 {
			continue
		}

		if HandleCommand(input[0], input[1:]) {
			lastCommandLine = commandLine
		}
	}
}
