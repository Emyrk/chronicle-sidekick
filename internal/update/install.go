package update

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"runtime"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

const repository = "Emyrk/chronicle-sidekick"

//go:embed update-signing-cert.pem
var updateSigningCertificate []byte

// Installer downloads, authenticates, and installs release artifacts for the
// current operating system and architecture.
type Installer struct {
	updater *selfupdate.Updater
}

// NewInstaller creates an installer that requires both a SHA-256 entry in
// checksums.txt and a valid project-owned ECDSA signature on checksums.txt.
func NewInstaller() (*Installer, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, fmt.Errorf("create GitHub update source: %w", err)
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:        source,
		Validator:     newValidator(updateSigningCertificate),
		Filters:       []string{portableAssetFilter(runtime.GOOS, runtime.GOARCH)},
		UniversalArch: "all",
	})
	if err != nil {
		return nil, fmt.Errorf("create updater: %w", err)
	}
	return &Installer{updater: updater}, nil
}

// portableAssetFilter uniquely selects the executable/archive intended for
// in-place updates. Release pages also contain first-install artifacts such as
// Windows setup executables and macOS disk images, which must never replace the
// running executable directly.
func portableAssetFilter(goos, goarch string) string {
	extension := `\.tar\.gz`
	if goos == "windows" {
		extension = `\.exe`
	}
	name := "chronicle-sidekick-" + goos + "-" + goarch
	return `(?i)(?:^|/)` + regexp.QuoteMeta(name) + extension + `$`
}

func newValidator(certificate []byte) selfupdate.Validator {
	return selfupdate.NewChecksumWithECDSAValidator("checksums.txt", certificate)
}

func shouldInstall(current, latest string, force bool) bool {
	return force || Newer(current, latest)
}

// InstallLatest replaces the running executable with the newest authenticated
// release. macOS app bundles are updated by Sparkle instead.
func (i *Installer) InstallLatest(ctx context.Context, current string, force bool) (string, error) {
	if runtime.GOOS == "darwin" {
		return "", errors.New("macOS updates are managed by Sparkle")
	}
	if current == "" || current == "dev" {
		return "", errors.New("development builds cannot update themselves")
	}
	release, found, err := i.updater.DetectLatest(ctx, selfupdate.ParseSlug(repository))
	if err != nil {
		return "", fmt.Errorf("find signed update: %w", err)
	}
	if !found {
		return "", errors.New("no signed update is available for this platform")
	}
	if !shouldInstall(current, release.Version(), force) {
		return "", fmt.Errorf("version %s is already current", current)
	}
	executable, err := selfupdate.ExecutablePath()
	if err != nil {
		return "", fmt.Errorf("locate Sidekick executable: %w", err)
	}
	if err := i.updater.UpdateTo(ctx, release, executable); err != nil {
		return "", fmt.Errorf("install version %s: %w", release.Version(), err)
	}
	return release.Version(), nil
}
