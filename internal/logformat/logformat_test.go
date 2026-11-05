package logformat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Emyrk/chronicle-sidekick/internal/logformat"
)

func TestChronicleWireValues(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1.12a-superwow-addon", string(logformat.LogFormat112aSuperwowAddon))
	require.Equal(t, "1.12a-cc-addon", string(logformat.LogFormat112aCcAddon))
	require.Equal(t, "3.3.5a-cc-addon", string(logformat.LogFormat335aCcAddon))
	require.Equal(t, "azerothcore-mod", string(logformat.LogFormatAzerothcoreMod))
	require.Equal(t, "2.4.3-cc-addon", string(logformat.LogFormat243CcAddon))
	require.Equal(t, "v9-cleu", string(logformat.LogFormatV9Cleu))
	require.Equal(t, "hermesproxy_1_14_2_cc", string(logformat.LogFormatHermesproxy1142Cc))
	require.Equal(t, "v22-cleu", string(logformat.LogFormatV22Cleu))
}

func TestSidekickFormats(t *testing.T) {
	t.Parallel()
	for _, format := range logformat.SidekickFormats() {
		require.True(t, format.ValidSidekick())
		require.NotEqual(t, "Unknown", format.Label())
	}
	require.True(t, logformat.LogFormatV22Cleu.ValidSidekick())
	require.False(t, logformat.LogFormatV9Cleu.ValidSidekick())
}
