package update

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"regexp"
	"testing"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedUpdateCertificate(t *testing.T) {
	block, _ := pem.Decode(updateSigningCertificate)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE", block.Type)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	_, ok := cert.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok)
}

func TestValidatorRequiresSignedChecksums(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test update key"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	validator := newValidator(certificate)

	asset := []byte("release artifact")
	assetHash := sha256.Sum256(asset)
	checksums := []byte(fmt.Sprintf("%x  Chronicle-Sidekick-linux-amd64.tar.gz\n", assetHash))
	checksumHash := sha256.Sum256(checksums)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, checksumHash[:])
	require.NoError(t, err)

	require.Equal(t, "checksums.txt", validator.GetValidationAssetName("Chronicle-Sidekick-linux-amd64.tar.gz"))
	require.NoError(t, validator.Validate("Chronicle-Sidekick-linux-amd64.tar.gz", asset, checksums))
	require.Equal(t, "checksums.txt.sig", validator.GetValidationAssetName("checksums.txt"))
	require.NoError(t, validator.Validate("checksums.txt", checksums, signature))
	tamperedChecksums := append([]byte(nil), checksums...)
	tamperedChecksums[0] ^= 1
	require.Error(t, validator.Validate("checksums.txt", tamperedChecksums, signature))

	recursive, ok := validator.(selfupdate.RecursiveValidator)
	require.True(t, ok)
	require.True(t, recursive.MustContinueValidation("checksums.txt"))
}

func TestShouldInstall(t *testing.T) {
	t.Parallel()
	require.True(t, shouldInstall("0.2.0", "0.3.0", false))
	require.False(t, shouldInstall("0.2.0", "0.2.0", false))
	require.False(t, shouldInstall("0.3.0", "0.2.0", false))
	require.True(t, shouldInstall("0.2.0", "0.2.0", true))
}

func TestPortableAssetFilterExcludesInstallers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos, goarch string
		match        []string
		reject       []string
	}{
		{
			goos: "windows", goarch: "amd64",
			match:  []string{"Chronicle-Sidekick-windows-amd64.exe", "https://example/releases/Chronicle-Sidekick-windows-amd64.exe"},
			reject: []string{"Chronicle-Sidekick-Setup-windows-amd64.exe", "Chronicle-Sidekick-windows-arm64.exe"},
		},
		{
			goos: "linux", goarch: "arm64",
			match:  []string{"Chronicle-Sidekick-linux-arm64.tar.gz"},
			reject: []string{"Chronicle-Sidekick-linux-arm64.zip", "Chronicle-Sidekick-linux-amd64.tar.gz"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.goos+"/"+test.goarch, func(t *testing.T) {
			re := regexp.MustCompile(portableAssetFilter(test.goos, test.goarch))
			for _, name := range test.match {
				require.Truef(t, re.MatchString(name), "expected %q to match", name)
			}
			for _, name := range test.reject {
				require.Falsef(t, re.MatchString(name), "expected %q not to match", name)
			}
		})
	}
}

func TestNewInstaller(t *testing.T) {
	installer, err := NewInstaller()
	require.NoError(t, err)
	require.NotNil(t, installer)
	require.NotNil(t, installer.updater)
}
