package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

func main() {
	version := flag.String("version", "dev", "release version suffix")
	flag.Parse()

	root, err := findRepoRoot()
	must(err)

	distRoot := filepath.Join(root, "dist")
	releaseDir := filepath.Join(distRoot, "LimitDock-"+*version)
	releaseZip := releaseDir + ".zip"

	must(os.RemoveAll(releaseDir))
	if err := os.Remove(releaseZip); err != nil && !errors.Is(err, os.ErrNotExist) {
		must(err)
	}
	for _, dir := range []string{
		releaseDir,
		filepath.Join(releaseDir, "assets"),
		filepath.Join(releaseDir, "docs", "images"),
		filepath.Join(releaseDir, "engine", "state"),
	} {
		must(os.MkdirAll(dir, 0o755))
	}

	run(root, "go", "test", "./...")
	run(root, "go", "build", "-ldflags", "-H windowsgui", "-o", filepath.Join(releaseDir, "LimitDock.exe"), ".\\cmd\\limitdock")

	must(copyDir(filepath.Join(root, "assets", "icons"), filepath.Join(releaseDir, "assets", "icons")))
	must(copyDir(filepath.Join(root, "docs", "images"), filepath.Join(releaseDir, "docs", "images")))
	for _, name := range []string{"README.md", "NOTES.md", "settings.example.json"} {
		must(copyFile(filepath.Join(root, name), filepath.Join(releaseDir, name)))
	}
	for _, name := range []string{"ARCHITECTURE.md", "EXE_PACKAGING.md", "PRODUCT_DESIGN.md", "USER_MANUAL.md"} {
		must(copyFile(filepath.Join(root, "docs", name), filepath.Join(releaseDir, "docs", name)))
	}
	must(copyFile(filepath.Join(root, "cmd", "limitdock", "LimitDock.exe.manifest"), filepath.Join(releaseDir, "LimitDock.exe.manifest")))

	must(zipDir(releaseDir, releaseZip))
	fmt.Println("Release prepared:", releaseDir)
	fmt.Println("Release archive:", releaseZip)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func run(root string, name string, args ...string) {
	fmt.Println(">", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".gocache"))
	must(cmd.Run())
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func zipDir(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isForbiddenReleaseEntry(rel) {
			return fmt.Errorf("refusing to package personal or runtime file: %s", rel)
		}
		if entry.IsDir() {
			_, err := zw.Create(rel + "/")
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		header.Method = zip.Deflate
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(writer, in)
		return err
	})
}

func isForbiddenReleaseEntry(rel string) bool {
	rel = strings.Trim(strings.ToLower(filepath.ToSlash(rel)), "/")
	if rel == "" {
		return false
	}
	if path.Base(rel) == "settings.json" {
		return true
	}
	return strings.HasPrefix(rel, "engine/state/")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
