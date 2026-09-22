package admincli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// Account is one row of the node's operator list as the CLI shows it.
type Account struct {
	RowKey           string
	Name             string
	Trust            peers.TrustLevel
	SigningPubKeyHex string
	// Source is "database" or "config"; config rows are edited in config.yaml.
	Source string
}

// ManageStore is a Store that can also list, remove and re-trust accounts.
type ManageStore interface {
	Store
	List(ctx context.Context) ([]Account, error)
	Remove(ctx context.Context, rowKey string) error
	SetTrust(ctx context.Context, rowKey string, trust peers.TrustLevel) error
}

// PrintAccounts writes the operator list, admins first.
func PrintAccounts(w io.Writer, accounts []Account) {
	if len(accounts) == 0 {
		fmt.Fprintln(w, "No accounts are enrolled on this node.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTRUST\tSIGN-IN KEY\tACCOUNT\tSOURCE")
	for _, admins := range []bool{true, false} {
		for _, a := range accounts {
			if (a.Trust >= peers.Admin) != admins {
				continue
			}
			key := a.SigningPubKeyHex
			if key == "" {
				key = "(bound at first sign-in)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Trust, key, shorten(a.RowKey), a.Source)
		}
	}
	tw.Flush()
}

// Resolve finds the one account a target names: its account key, its sign-in
// key, or a display name that no other account shares.
func Resolve(accounts []Account, target string) (Account, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return Account{}, errors.New("name an account: its display name, sign-in key or account key")
	}
	for _, a := range accounts {
		if a.RowKey == target || (a.SigningPubKeyHex != "" && strings.EqualFold(a.SigningPubKeyHex, target)) {
			return a, nil
		}
	}
	var named []Account
	for _, a := range accounts {
		if strings.EqualFold(strings.TrimSpace(a.Name), target) {
			named = append(named, a)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return Account{}, fmt.Errorf("no account matches %q", target)
	}
	return Account{}, fmt.Errorf("%d accounts are named %q; name one by its sign-in key instead", len(named), target)
}

// Remove deletes the account a target names, after confirmation.
func Remove(ctx context.Context, term Terminal, store ManageStore, target string, yes bool) error {
	account, err := resolveEditable(ctx, store, target)
	if err != nil {
		return err
	}
	if !yes {
		ok, err := askYes(term, fmt.Sprintf("Remove %s (%s)? [y/N] ", account.Name, account.Trust))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled")
		}
	}
	if err := store.Remove(ctx, account.RowKey); err != nil {
		return err
	}
	fmt.Fprintf(term.Out, "Removed %s.\n", account.Name)
	return nil
}

// SetTrust changes the trust of the account a target names.
func SetTrust(ctx context.Context, term Terminal, store ManageStore, target, level string, yes bool) error {
	trust, err := peers.ParseTrustLevel(strings.ToLower(strings.TrimSpace(level)))
	if err != nil {
		return err
	}
	if trust == peers.Never || trust >= peers.Ultimate {
		return fmt.Errorf("trust %q cannot be assigned; choose unknown, marginal, standard, full or admin", trust)
	}
	account, err := resolveEditable(ctx, store, target)
	if err != nil {
		return err
	}
	if account.Trust == trust {
		fmt.Fprintf(term.Out, "%s is already %s.\n", account.Name, trust)
		return nil
	}
	if !yes {
		ok, err := askYes(term, fmt.Sprintf("Change %s from %s to %s? [y/N] ", account.Name, account.Trust, trust))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("cancelled")
		}
	}
	if err := store.SetTrust(ctx, account.RowKey, trust); err != nil {
		return err
	}
	fmt.Fprintf(term.Out, "%s is now %s.\n", account.Name, trust)
	return nil
}

func resolveEditable(ctx context.Context, store ManageStore, target string) (Account, error) {
	accounts, err := store.List(ctx)
	if err != nil {
		return Account{}, err
	}
	account, err := Resolve(accounts, target)
	if err != nil {
		return Account{}, err
	}
	if account.Source == "config" {
		return Account{}, fmt.Errorf("%s is set in config.yaml; edit it there", account.Name)
	}
	return account, nil
}

func shorten(s string) string {
	if len(s) <= 24 {
		return s
	}
	return s[:14] + "…" + s[len(s)-6:]
}

// List returns every operator row, database and config.
func (s AuthDBStore) List(context.Context) ([]Account, error) {
	users, err := s.Users.ListUsers()
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(users))
	for _, u := range users {
		accounts = append(accounts, Account{RowKey: u.XPub, Name: u.Name, Trust: u.TrustLevel, SigningPubKeyHex: u.SigningPubKeyHex, Source: u.Source})
	}
	return accounts, nil
}

func (s AuthDBStore) Remove(_ context.Context, rowKey string) error { return s.Users.RemoveUser(rowKey) }

func (s AuthDBStore) SetTrust(_ context.Context, rowKey string, trust peers.TrustLevel) error {
	return s.Users.UpdateTrust(rowKey, trust)
}
