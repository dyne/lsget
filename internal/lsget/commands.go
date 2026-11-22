package lsget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path"
	"strings"
)

type execReq struct {
	Input       string `json:"input"`
	LiteralPath bool   `json:"literalPath,omitempty"`
}

type execResp struct {
	Output          string               `json:"output"`
	Truncated       bool                 `json:"truncated,omitempty"`
	Download        string               `json:"download,omitempty"`
	CWD             string               `json:"cwd,omitempty"`
	Readme          *string              `json:"readme,omitempty"`
	DocType         string               `json:"docType,omitempty"`
	ReadmeTruncated bool                 `json:"readmeTruncated,omitempty"`
	Clipboard       string               `json:"clipboard,omitempty"`
	HTML            string               `json:"html,omitempty"`
	Help            bool                 `json:"help,omitempty"`
	Image           *execImage           `json:"image,omitempty"`
	Redirect        string               `json:"redirect,omitempty"`
	Listing         *terminalListingView `json:"listing,omitempty"`
}

type execImage struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Size string `json:"size"`
}

type terminalCommand struct {
	server       *server
	writer       http.ResponseWriter
	request      *http.Request
	name         string
	args         []string
	requestedCWD string
	literalPath  bool
	root         *os.Root
	budget       *workBudget
	releaseWork  func()
	session      *session
	cwd          string
}

func (s *server) handleExec(w http.ResponseWriter, r *http.Request) {
	var req execReq
	if !s.readCommandRequest(w, r, &req) {
		return
	}
	args := parseArgs(strings.TrimSpace(req.Input))
	if len(args) == 0 {
		_ = json.NewEncoder(w).Encode(execResp{Output: ""})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	command := terminalCommand{
		server: s, writer: w, request: r, name: args[0], args: args[1:],
		requestedCWD: requestCWD(r), literalPath: req.LiteralPath, budget: newWorkBudget(),
	}
	defer command.finish()

	switch command.name {
	case "pwd":
		command.printWorkingDirectory()
	case "help":
		command.respond(execResp{HTML: renderHelp(), Help: true})
	case "ls", "dir":
		command.runWithFilesystemWork(command.list)
	case "cd":
		command.runWithFilesystemWork(command.changeDirectory)
	case "cat":
		command.runWithFilesystemWork(command.cat)
	case "get", "rget", "wget", "download":
		command.download()
	case "url", "share":
		command.runWithRoot(command.share)
	case "sum", "checksum":
		command.runWithFilesystemWork(command.checksum)
	case "tree":
		command.runWithFilesystemWork(command.tree)
	case "find":
		command.runWithFilesystemWork(command.find)
	case "grep":
		command.runWithFilesystemWork(command.grep)
	default:
		command.respond(execResp{Output: fmt.Sprintf("sh: %s: command not found", command.name)})
	}
}

func (c *terminalCommand) respond(response execResp) {
	_ = json.NewEncoder(c.writer).Encode(response)
}

func (c *terminalCommand) runWithFilesystemWork(run func()) {
	c.releaseWork = c.server.reserveWork(c.writer, c.request)
	if c.releaseWork != nil {
		c.runWithRoot(run)
	}
}

func (c *terminalCommand) runWithRoot(run func()) {
	root, err := os.OpenRoot(c.server.rootAbs)
	if err != nil {
		c.respond(execResp{Output: c.name + ": cannot open served directory"})
		return
	}
	defer func() { _ = root.Close() }()
	c.root = root
	run()
}

func (c *terminalCommand) finish() {
	if c.session != nil {
		c.server.releaseSession(c.session)
	}
	if c.releaseWork != nil {
		c.releaseWork()
	}
}

// A command may resolve several paths. Load one snapshot on its first relative path.
func (c *terminalCommand) cwdFor(target string) (string, bool) {
	if path.IsAbs(target) {
		return "/", true
	}
	if c.requestedCWD != "" {
		return c.requestedCWD, true
	}
	if c.session == nil {
		c.session = c.server.acquireSession(c.writer, c.request)
		if c.session == nil {
			return "", false
		}
		c.cwd = c.session.workingDirectory()
	}
	return c.cwd, true
}

// parseArgs handles single and double quotes, with backslash escapes inside double quotes.
func parseArgs(line string) []string {
	var args []string
	var buf bytes.Buffer
	inSingle, inDouble := false, false

	flush := func() {
		if buf.Len() > 0 || inSingle || inDouble {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(line) {
				i++
				buf.WriteByte(line[i])
			} else {
				buf.WriteByte(c)
			}
			continue
		}
		switch c {
		case ' ', '\t', '\n':
			if buf.Len() > 0 {
				args = append(args, buf.String())
				buf.Reset()
			}
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		default:
			buf.WriteByte(c)
		}
	}
	flush()
	return args
}

const helpTpl = `Welcome to <span class="ps1">lsget</span> <span class="help-version">v{{.Version}}</span>!
<span class="help-note">Type one of the commands below to get started.</span>
<br/>

<span class="help-heading">Available commands:</span>
• <strong>help</strong> - <span class="help-description">print this message again</span>
• <strong>pwd</strong> - <span class="help-description">print working directory</span>
• <strong>ls</strong> <span class="help-note">[-l] [-h]</span>|<strong>dir</strong> <span class="help-note">[-l] [-h]</span> - <span class="help-description">list files (-h for human readable sizes)</span>
• <strong>cd</strong> <span class="help-note">DIR</span> - <span class="help-description">change directory</span>
• <strong>cat</strong> <span class="help-note">FILE</span> - <span class="help-description">view a text file</span>
• <strong>sum</strong>|<strong>checksum</strong> <span class="help-note">FILE</span> - <span class="help-description">print MD5 and SHA256 checksums</span>
• <strong>get</strong>|<strong>wget</strong>|<strong>download</strong> <span class="help-note">FILE</span> - <span class="help-description">download a file</span>
• <strong>url</strong>|<strong>share</strong> <span class="help-note">FILE</span> - <span class="help-description">get a shareable URL with a Copy URL control</span>
• <strong>tree</strong> <span class="help-note">[-L&lt;DEPTH&gt;] [-a]</span> - <span class="help-description">directory structure</span>
• <strong>find</strong> <span class="help-note">[PATH] [-name PATTERN] [-type f|d]</span> - <span class="help-description">search for files and directories</span>
• <strong>grep</strong> <span class="help-note">[-r] [-i] [-n] PATTERN [FILE...]</span> - <span class="help-description">search for text patterns in files</span>

<br/><br/>
<span class="help-heading"><strong>Complete</strong> suggests file and directory names. It becomes <strong>Enter</strong> when the command is ready to run.</span>
`

func renderHelp() string {
	helpMessage := template.Must(template.New("help").Parse(helpTpl))
	var b bytes.Buffer
	_ = helpMessage.Execute(&b, struct{ Version string }{Version: version})
	return b.String()
}
