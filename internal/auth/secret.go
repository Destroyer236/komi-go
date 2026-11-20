// Package auth stores the GitHub token in Windows Credential Manager instead of
// a plaintext file, so a stolen AppData copy does not leak the user's PAT.
package auth

import (
	"errors"
	"strings"

	"github.com/danieljoos/wincred"
)

const target = "komi-go/github-token"

// SaveToken writes the PAT to Credential Manager (generic credential, local
// machine scope). An empty token deletes the entry.
func SaveToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return DeleteToken()
	}
	cred := wincred.NewGenericCredential(target)
	cred.UserName = "github-pat"
	cred.CredentialBlob = []byte(token)
	cred.Persist = wincred.PersistLocalMachine
	return cred.Write()
}

// LoadToken returns the stored PAT or an empty string.
func LoadToken() (string, error) {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		// ErrElementNotFound: first run, not an error for the UI.
		if errors.Is(err, wincred.ErrElementNotFound) {
			return "", nil
		}
		return "", err
	}
	return string(cred.CredentialBlob), nil
}

// DeleteToken removes the stored PAT.
func DeleteToken() error {
	cred := wincred.NewGenericCredential(target)
	err := cred.Delete()
	if err == nil {
		return nil
	}
	if errors.Is(err, wincred.ErrElementNotFound) {
		return nil
	}
	return errors.New("无法删除已保存的令牌：" + err.Error())
}
