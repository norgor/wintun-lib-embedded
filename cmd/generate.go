//+build ignore

package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"go/format"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const gitRepo = "git://git.zx2c4.com/wintun"
const gitDir = ".git-wintun"
const generateDir = "."

var outputPath = fmt.Sprintf("./embedded/binary_%s_%s.go", runtime.GOOS, runtime.GOARCH)
var goarchToWintunArch = map[string]string{
	"amd64": "amd64",
	"arm":   "arm",
	"arm64": "arm64",
	"386":   "x86",
}

var tplFuncs = map[string]interface{}{
	"byteize": byteize,
}
var tpl = template.Must(template.New("").Funcs(tplFuncs).Parse(`// Code generated by wintun-lib-embedded; DO NOT EDIT.
package wintunlib

var binary = []byte{ {{ byteize . }} }

// GetBinary returns the Wintun library according to GOARCH (compile time).
// The library can be written to file as a dll file and thereafter it can be loaded.
func GetBinary() []byte { return binary }

`))

func byteize(data []byte) string {
	sb := strings.Builder{}
	for _, v := range data {
		sb.WriteString(fmt.Sprintf("%d,", int(v)))
	}
	return sb.String()
}

func runWithOut(cmd *exec.Cmd) (out string, err error) {
	outb, err := cmd.CombinedOutput()
	out = string(outb)
	code := cmd.ProcessState.ExitCode()
	if code != -1 && code != 0 {
		return "", fmt.Errorf("exit code %d: %s", code, out)
	}
	if err != nil {
		return "", err
	}
	return out, nil
}

func identifyLatestVersion() (string, error) {
	os.RemoveAll(gitDir)
	cloneCmd := exec.Command("git", "clone", "--no-checkout", gitRepo, gitDir)
	if _, err := runWithOut(cloneCmd); err != nil {
		return "", fmt.Errorf("unable to clone Wintun's git: %w", err)
	}
	verCmd := exec.Command("git", "--git-dir", fmt.Sprintf("%s/.git", gitDir), "describe", "--tags", "--abbrev=0")
	verOut, err := runWithOut(verCmd)
	if err != nil {
		return "", fmt.Errorf("failed to get version from git repo: %w", err)
	}
	if err := os.RemoveAll(gitDir); err != nil {
		return "", fmt.Errorf("failed to remove Wintun's git directory: %s", err)
	}
	return strings.TrimSpace(verOut), nil
}

func normalizeVersion(ver string) (string, error) {
	trimVer := strings.TrimSpace(ver)
	if trimVer == "" {
		return "", fmt.Errorf("invalid version '%s'", trimVer)
	}
	split := strings.SplitN(trimVer, ".", 3)
	for i := len(split) - 1; i < 3; i++ {
		split = append(split, "0")
	}
	return fmt.Sprintf(
		"%s.%s.%s",
		split[0],
		split[1],
		strings.ReplaceAll(split[2], ".", "_"),
	), nil
}

func downloadUrl(version string) string {
	log.Printf("downloading version %s", version)
	return fmt.Sprintf("https://www.wintun.net/builds/wintun-%s.zip", version)
}

func unzipBinaries(zipReader io.ReaderAt, zipLen int) (map[string][]byte, error) {
	r, err := zip.NewReader(zipReader, int64(zipLen))
	if err != nil {
		return nil, fmt.Errorf("unable to create zip reader: %w", err)
	}
	fmap := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		fmap[f.Name] = f
	}

	bmap := make(map[string][]byte, len(goarchToWintunArch))
	for k, v := range goarchToWintunArch {
		f := fmap[fmt.Sprintf("wintun/bin/%s/wintun.dll", v)]
		fh, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("unable to open binary for GOARCH %s", k)
		}
		data, err := ioutil.ReadAll(fh)
		fh.Close()
		bmap[k] = data
		if err != nil {
			return nil, fmt.Errorf("unable to read binary for GOARCH %s", k)
		}
	}
	return bmap, nil
}

func generateFileForArch(arch string, binary []byte) error {
	if err := os.MkdirAll(generateDir, os.ModePerm); err != nil {
		return fmt.Errorf("unable to create generate dir: %w", err)
	}
	out := bytes.Buffer{}
	if err := tpl.Execute(&out, binary); err != nil {
		return fmt.Errorf("unable to execute template: %w", err)
	}
	fmtOut, err := format.Source(out.Bytes())
	if err != nil {
		return fmt.Errorf("unable to format template output: %w", err)
	}
	fname := filepath.Join(generateDir, fmt.Sprintf("lib_windows_%s.go", arch))
	if err := ioutil.WriteFile(fname, fmtOut, os.ModePerm); err != nil {
		return fmt.Errorf("unable to write output file: %w", err)
	}
	return nil
}

func hasUncommittedChanges() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain=v1")
	out, err := runWithOut(cmd)
	if err != nil {
		return false, fmt.Errorf("unable to check git status: %w", err)
	}
	return len(strings.TrimSpace(out)) > 0, nil
}

func pushToGit(ver string) error {
	addCmd := exec.Command("git", "add", ".")
	if _, err := runWithOut(addCmd); err != nil {
		return fmt.Errorf("unable to git add: %w", err)
	}
	commitCmd := exec.Command("git", "commit", "-m", fmt.Sprintf("updated to Wintun version %s", ver))
	if _, err := runWithOut(commitCmd); err != nil {
		return fmt.Errorf("unable to create commit: %w", err)
	}
	tag := fmt.Sprintf("v%s", ver)
	tagCmd := exec.Command("git", "tag", "-f", "-a", tag, "-m", fmt.Sprintf("Wintun version %s", ver))
	if _, err := runWithOut(tagCmd); err != nil {
		return fmt.Errorf("unable to create git tag: %w", err)
	}
	pushCmd := exec.Command("git", "push", "--follow-tags")
	if _, err := runWithOut(pushCmd); err != nil {
		return fmt.Errorf("unable to push: %w", err)
	}
	tagPushCmd := exec.Command("git", "push", "origin", tag)
	if _, err := runWithOut(tagPushCmd); err != nil {
		return fmt.Errorf("unable to push tag: %w", err)
	}
	return nil
}

func main() {
	log.Println("identifying latest Wintun version...")
	wtver, err := identifyLatestVersion()
	if err != nil {
		log.Fatalf("unable to identify latest Wintun version")
	}

	ver, err := normalizeVersion(wtver)
	if err != nil {
		log.Fatalf("failed to normalize version: %v", err)
	}

	log.Printf("found ver %s (normalized %s)! downloading...", wtver, ver)
	url := downloadUrl(wtver)
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("downloading Wintun failed: %v", err)
	}

	defer resp.Body.Close()
	zip, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("unable to read response: %v", err)
	}
	log.Println("unzipping binaries")
	bins, err := unzipBinaries(bytes.NewReader(zip), len(zip))
	if err != nil {
		log.Fatalf("unable to unzip Wintun binaries: %v", err)
	}

	log.Println("generating source files...")
	for k := range goarchToWintunArch {
		log.Printf(" - generating for %s", k)
		if err := generateFileForArch(k, bins[k]); err != nil {
			log.Fatalf("unable to generate file: %v", err)
		}
	}

	hasChanges, err := hasUncommittedChanges()
	if err != nil {
		log.Fatalf("unable to check for uncommitted changes: %v", err)
	}

	if hasChanges {
		log.Println("changes detected, pushing...")
		if err := pushToGit(ver); err != nil {
			log.Fatalf("unable to push changes: %v", err)
		}
	}

	log.Println("done!")
}
