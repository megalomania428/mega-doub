// Package command is documented in command.go.
package command

import (
	"log/slog"
	"time"

	"github.com/megalomania428/mega-doub/pkg/config"
	"github.com/megalomania428/mega-doub/pkg/secret"
)

// vaultOptions returns the settings of the Vault client of the run: the vault section of
// the configuration, global.retry with the overrides from the environment and the logger.
func vaultOptions(loaded *config.Loaded, logger *slog.Logger) secret.VaultOptions {
	v := loaded.Config.Vault
	return secret.VaultOptions{Address: v.Address, Token: v.Token,
		Timeout: time.Duration(v.Timeout), CAFile: v.CAFile, Retry: loaded.Retry,
		Logger: logger}
}
