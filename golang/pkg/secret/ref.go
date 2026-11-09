// Package secret holds references to secret values.
package secret

// Ref is a reference to a secret value, such as env:NAME or vault:mount/path/key.
type Ref string
