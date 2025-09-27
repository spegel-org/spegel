package option

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApply(t *testing.T) {
	t.Parallel()

	type TestConfig struct {
		Data string
	}
	type TestOption = Option[TestConfig]
	withData := func(data string) TestOption {
		return func(cfg *TestConfig) error {
			cfg.Data = data
			return nil
		}
	}
	withError := func(err error) TestOption {
		return func(cfg *TestConfig) error {
			return err
		}
	}

	cfg := TestConfig{}
	err := Apply(&cfg)
	require.NoError(t, err)
	require.Empty(t, cfg.Data)

	err = Apply(&cfg, nil)
	require.NoError(t, err)
	require.Empty(t, cfg.Data)

	cfgErr := errors.New("hello world")
	err = Apply(&cfg, withError(cfgErr))
	require.Error(t, err)
	require.Equal(t, cfgErr, err)

	err = Apply(&cfg, withData("foo bar"))
	require.NoError(t, err)
	require.Equal(t, "foo bar", cfg.Data)
}
