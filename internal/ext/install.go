package ext

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/axitdev/onfly/internal/config"
)

type Options struct {
	Dir, Extensions, Config string
	Yes, IsTTY              bool
	Stdin                   io.Reader
	Stdout, Stderr          io.Writer
	Client                  *http.Client
}

type installRef struct {
	Local   bool
	Name    string
	Version string
	Path    string
}

func projectOf(opt Options) (cfgPath, root string, err error) {
	if strings.TrimSpace(opt.Config) != "" {
		cfgPath, err = filepath.Abs(opt.Config)
		if err != nil {
			return "", "", err
		}
		return cfgPath, config.ProjectRoot(cfgPath), nil
	}
	cfgPath, err = config.NearestConfig(opt.Dir)
	if err != nil {
		return "", "", err
	}
	return cfgPath, config.ProjectRoot(cfgPath), nil
}

func Install(ctx context.Context, opt Options, arg string) error {
	ref, err := parseInstallArg(arg)
	if err != nil {
		return err
	}
	if !ref.Local && strings.TrimSpace(opt.Extensions) != "" {
		return fmt.Errorf("ext: --extensions is only valid for a local wasm file")
	}
	cfgPath, root, err := projectOf(opt)
	if err != nil {
		return err
	}
	var version, sum, file, source, temp string
	var exts []string
	if ref.Local {
		if strings.TrimSpace(opt.Extensions) == "" {
			return fmt.Errorf("ext: --extensions is required for a local wasm file")
		}
		exts, err = splitExtensions(opt.Extensions)
		if err != nil {
			return err
		}
		file, source = filepath.Base(ref.Path), "local"
		temp, sum, err = stageLocal(ref.Path)
	} else {
		manifest, ferr := FetchManifest(ctx, opt.Client)
		if ferr != nil {
			return ferr
		}
		var rel Release
		version, rel, err = Resolve(manifest, ref.Name, ref.Version)
		if err != nil {
			return err
		}
		exts, file, source = rel.Extensions, ref.Name+"-parser.wasm", "registry"
		relPath := ".onfly/plugins/" + file
		ok, cerr := alreadyCurrent(root, cfgPath, ref.Name, version, relPath, exts)
		if cerr != nil {
			return cerr
		}
		if ok {
			fmt.Fprintf(opt.Stdout, "%s %s is current\n", ref.Name, version)
			return nil
		}
		temp, sum, err = Download(ctx, opt.Client, rel.URL, rel.SHA256)
	}
	if err != nil {
		return err
	}
	defer func() {
		if temp != "" {
			os.Remove(temp)
		}
	}()
	relPath := ".onfly/plugins/" + file
	conflicts, err := config.ParserConflicts(cfgPath, config.WasmParser{Extensions: exts, Path: relPath})
	if err != nil {
		return err
	}
	accepted := false
	if len(conflicts) > 0 {
		var cerr error
		accepted, cerr = confirmReplace(opt, conflicts)
		if cerr != nil || !accepted {
			if cerr != nil {
				return cerr
			}
			return fmt.Errorf("ext: install cancelled")
		}
	}
	return withPluginLock(root, func() error {
		now, cerr := config.ParserConflicts(cfgPath, config.WasmParser{Extensions: exts, Path: relPath})
		if cerr != nil {
			return cerr
		}
		if len(now) > 0 && !opt.Yes && (!accepted || !sameStringSet(now, conflicts)) {
			return fmt.Errorf("parser for %q already exists; rerun with --yes", strings.Join(now, ", "))
		}
		lock, lerr := LoadLock(root)
		if lerr != nil {
			return lerr
		}
		prevFile := ""
		if prev, ok := lock[ref.Name]; ok {
			prevFile = prev.File
		}
		for other, item := range lock {
			if other == ref.Name || item.File != file {
				continue
			}
			if !opt.Yes {
				return fmt.Errorf("ext: %s already provides %s; rerun with --yes", other, file)
			}
			delete(lock, other)
		}
		dest := filepath.Join(root, ".onfly", "plugins", file)
		if err := MoveInto(temp, dest); err != nil {
			return err
		}
		temp = ""
		prevCfg, err := os.ReadFile(cfgPath)
		if err != nil {
			_ = RestoreBackup(dest)
			return err
		}
		rolled := true
		oldPath := ""
		parked := ""
		defer func() {
			if !rolled {
				if parked != "" {
					_ = os.Remove(parked)
				}
				return
			}
			_ = writeAtomic(cfgPath, prevCfg)
			_ = RestoreBackup(dest)
			if parked != "" {
				_ = os.Rename(parked, oldPath)
			}
		}()
		if prevFile != "" && prevFile != file && pluginFileOK(prevFile) {
			oldPath = filepath.Join(root, ".onfly", "plugins", prevFile)
			parked = oldPath + ".removing"
			if err := os.Rename(oldPath, parked); err != nil {
				if os.IsNotExist(err) {
					parked = ""
				} else {
					return err
				}
			}
			if err := config.RemoveWasmParser(cfgPath, ".onfly/plugins/"+prevFile); err != nil {
				return err
			}
		}
		if err := config.UpsertWasmParser(cfgPath, config.WasmParser{Extensions: exts, Path: relPath}); err != nil {
			return err
		}
		lock[ref.Name] = Installed{Version: version, File: file, SHA256: sum, Source: source, Extensions: exts}
		if err := lockSaver(root, lock); err != nil {
			return err
		}
		rolled = false
		if err := DiscardBackup(dest); err != nil {
			return err
		}
		label := version
		if source == "local" {
			label = "local"
		}
		fmt.Fprintf(opt.Stdout, "installed %s %s\n", ref.Name, label)
		return nil
	})
}

var lockSaver = SaveLock

func Update(ctx context.Context, opt Options, name string) error {
	_, root, err := projectOf(opt)
	if err != nil {
		return err
	}
	lock, err := LoadLock(root)
	if err != nil {
		return err
	}
	if name != "" {
		return updateOne(ctx, opt, lock, name, true)
	}
	names := make([]string, 0, len(lock))
	for n := range lock {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := updateOne(ctx, opt, lock, n, false); err != nil {
			return err
		}
	}
	return nil
}

func updateOne(ctx context.Context, opt Options, lock map[string]Installed, name string, explicit bool) error {
	item, ok := lock[name]
	if !ok {
		return fmt.Errorf("ext: plugin %q is not installed", name)
	}
	if item.Source != "registry" {
		if explicit {
			return fmt.Errorf("ext: %s is a local plugin", name)
		}
		fmt.Fprintf(opt.Stdout, "skipped %s (local)\n", name)
		return nil
	}
	manifest, err := FetchManifest(ctx, opt.Client)
	if err != nil {
		return err
	}
	latest, _, err := Resolve(manifest, name, "")
	if err != nil {
		return err
	}
	if item.Version != "" && versionLess(latest, item.Version) {
		fmt.Fprintf(opt.Stdout, "%s %s is newer than latest %s\n", name, item.Version, latest)
		return nil
	}
	return Install(ctx, opt, name)
}

func Remove(opt Options, name string) error {
	cfgPath, root, err := projectOf(opt)
	if err != nil {
		return err
	}
	return withPluginLock(root, func() error {
		lock, err := LoadLock(root)
		if err != nil {
			return err
		}
		item, ok := lock[name]
		if !ok {
			return fmt.Errorf("ext: plugin %q is not installed", name)
		}
		if !pluginFileOK(item.File) {
			return fmt.Errorf("ext: plugin %q has an invalid file name", name)
		}
		prevCfg, err := os.ReadFile(cfgPath)
		if err != nil {
			return err
		}
		wasm := filepath.Join(root, ".onfly", "plugins", item.File)
		bak := wasm + ".removing"
		renamed := false
		if err := os.Rename(wasm, bak); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else {
			renamed = true
		}
		rolled := true
		defer func() {
			if !rolled {
				if renamed {
					_ = os.Remove(bak)
				}
				return
			}
			_ = writeAtomic(cfgPath, prevCfg)
			if renamed {
				_ = os.Rename(bak, wasm)
			}
		}()
		if err := config.RemoveWasmParser(cfgPath, ".onfly/plugins/"+item.File); err != nil {
			return err
		}
		delete(lock, name)
		for other, kept := range lock {
			if kept.File == item.File {
				delete(lock, other)
			}
		}
		if err := lockSaver(root, lock); err != nil {
			return err
		}
		rolled = false
		fmt.Fprintf(opt.Stdout, "removed %s\n", name)
		return nil
	})
}

func List(ctx context.Context, opt Options) error {
	_, root, err := projectOf(opt)
	if err != nil {
		return err
	}
	lock, err := LoadLock(root)
	if err != nil {
		return err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	manifest, regErr := FetchManifest(fetchCtx, opt.Client)
	latest := map[string]string{}
	if regErr == nil {
		for name, plugin := range manifest {
			latest[name] = plugin.Latest
		}
	}
	names := make([]string, 0, len(lock))
	for name := range lock {
		names = append(names, name)
	}
	sort.Strings(names)
	tw := tabwriter.NewWriter(opt.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tSTATUS")
	for _, name := range names {
		item := lock[name]
		ver := item.Version
		if item.Source != "registry" || ver == "" {
			ver = "local"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, ver, pluginStatus(root, item, latest[name], regErr == nil))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if regErr != nil {
		fmt.Fprintf(opt.Stdout, "registry: %s\n", regErr)
	}
	return nil
}

func pluginStatus(root string, item Installed, latest string, registryOK bool) string {
	if !pluginFileOK(item.File) {
		return "corrupted"
	}
	path := filepath.Join(root, ".onfly", "plugins", item.File)
	got, err := HashFile(path)
	if err != nil || got != item.SHA256 {
		return "corrupted"
	}
	if registryOK && item.Source == "registry" && latest != "" && versionLess(item.Version, latest) {
		return "outdated"
	}
	return "active"
}

func pluginFileOK(file string) bool {
	return file != "" && file != "." && file != ".." && !strings.ContainsAny(file, `/\`) && file == filepath.Base(file)
}

func confirmReplace(opt Options, conflicts []string) (bool, error) {
	list := strings.Join(conflicts, ", ")
	if opt.Yes {
		return true, nil
	}
	if !opt.IsTTY {
		return false, fmt.Errorf("parser for %q already exists; rerun with --yes", list)
	}
	out := opt.Stderr
	if out == nil {
		out = opt.Stdout
	}
	fmt.Fprintf(out, "Replace parser for %s? [y/N] ", list)
	line, err := bufio.NewReader(opt.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func parseInstallArg(arg string) (installRef, error) {
	if strings.TrimSpace(arg) == "" {
		return installRef{}, fmt.Errorf("ext: plugin name is required")
	}
	if isLocalPath(arg) {
		name := strings.TrimSuffix(filepath.Base(arg), ".wasm")
		if !validName(name) {
			return installRef{}, fmt.Errorf("ext: plugin name %q is invalid", name)
		}
		return installRef{Local: true, Name: name, Path: arg}, nil
	}
	name, ver, ok := strings.Cut(arg, "@")
	if ok && ver == "" {
		return installRef{}, fmt.Errorf("ext: plugin version is required")
	}
	if !validName(name) {
		return installRef{}, fmt.Errorf("ext: plugin name %q is invalid", name)
	}
	return installRef{Name: name, Version: ver}, nil
}

func isLocalPath(arg string) bool {
	return strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "/") || strings.ContainsAny(arg, `/\`) || strings.HasSuffix(strings.ToLower(arg), ".wasm")
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

func splitExtensions(flag string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(flag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ext: --extensions is required for a local wasm file")
	}
	return out, nil
}

func stageLocal(src string) (string, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", "", err
	}
	if !st.Mode().IsRegular() {
		return "", "", fmt.Errorf("ext: %s is not a regular file", src)
	}
	f, err := os.CreateTemp("", "onfly_download_*")
	if err != nil {
		return "", "", err
	}
	path := f.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(in, maxPluginBytes+1))
	closeErr := f.Close()
	if n > maxPluginBytes {
		os.Remove(path)
		return "", "", fmt.Errorf("ext: plugin exceeds 64MiB")
	}
	if err != nil || closeErr != nil {
		os.Remove(path)
		if err != nil {
			return "", "", err
		}
		return "", "", closeErr
	}
	return path, fmt.Sprintf("%x", h.Sum(nil)), nil
}

func alreadyCurrent(root, cfgPath, name, version, relPath string, exts []string) (bool, error) {
	lock, err := LoadLock(root)
	if err != nil {
		return false, err
	}
	item, ok := lock[name]
	if !ok || item.Source != "registry" || item.Version != version || item.File != filepath.Base(relPath) {
		return false, nil
	}
	got, err := HashFile(filepath.Join(root, ".onfly", "plugins", item.File))
	if err != nil || !strings.EqualFold(got, item.SHA256) {
		return false, nil
	}
	entries, err := config.WasmParsers(cfgPath)
	if err != nil {
		return false, err
	}
	want, err := normalizeExts(exts)
	if err != nil {
		return false, err
	}
	wantPath := filepath.ToSlash(relPath)
	for _, p := range entries {
		if p.Path != wantPath {
			continue
		}
		have, err := normalizeExts(p.Extensions)
		if err != nil {
			return false, err
		}
		if sameStrings(have, want) {
			return true, nil
		}
	}
	return false, nil
}

func normalizeExts(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, ext := range in {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if ext == "." {
			return nil, fmt.Errorf("ext: extension is invalid")
		}
		if !seen[ext] {
			seen[ext] = true
			out = append(out, ext)
		}
	}
	return out, nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	return sameStrings(left, right)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
