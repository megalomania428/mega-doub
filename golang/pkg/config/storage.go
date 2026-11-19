// cspell:ignore acd duplicacy gcd gcs hubic minio minios odb sftpc smb storj wasabi
// cspell:ignore webdav

// Package config is documented in config.go.
package config

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// storageURLPattern is the pattern by which duplicacy splits a storage URL that is not
// a local path: the type, the user information with its @, the host and the path. Like
// the one of duplicacy, it is not anchored at the end.
var storageURLPattern = regexp.MustCompile(`^([\w-]+)://([\w\-@\.\!]+@)?([^/]+)(/(.+))?`)

// storageTypePattern is the start of a storage URL with its type.
var storageTypePattern = regexp.MustCompile(`^([\w+.-]+)://`)

// b2CustomPattern is the second pattern of duplicacy for b2-custom, which needs a
// bucket after the download host.
var b2CustomPattern = regexp.MustCompile(`^b2-custom://([^/]+)/([^/]+)(/(.+))?`)

// fileStorage is the storage type of a local path: an absolute path, flat:// or
// samba://. It takes no keys.
const fileStorage = "file"

// storageType holds the names of storage.keys of a storage type without the prefix of
// storage.name: the required ones in the order of the problems and the optional ones.
type storageType struct {
	required []string
	optional []string
}

// sshKeys are the optional keys of sftp and sftpc.
var sshKeys = []string{"ssh_password", "ssh_key_file", "ssh_passphrase"}

// storageTypes are the types of storage.url that duplicacy creates a storage of, by
// the scheme as written: duplicacy compares it case-sensitively.
var storageTypes = map[string]storageType{
	"sftp":      {optional: sshKeys},
	"sftpc":     {optional: sshKeys},
	"s3":        {required: []string{"s3_id", "s3_secret"}},
	"s3c":       {required: []string{"s3_id", "s3_secret"}},
	"minio":     {required: []string{"s3_id", "s3_secret"}},
	"minios":    {required: []string{"s3_id", "s3_secret"}},
	"wasabi":    {required: []string{"wasabi_key", "wasabi_secret"}},
	"dropbox":   {required: []string{"dropbox_token"}},
	"b2":        {required: []string{"b2_id", "b2_key"}},
	"b2-custom": {required: []string{"b2_id", "b2_key"}},
	"azure":     {required: []string{"azure_key"}},
	"acd":       {required: []string{"acd_token"}},
	"gcs":       {required: []string{"gcs_token"}},
	"gcd":       {required: []string{"gcd_token"}},
	"one": {required: []string{"one_token"},
		optional: []string{"one_client_id", "one_client_secret"}},
	"odb": {required: []string{"odb_token"},
		optional: []string{"odb_client_id", "odb_client_secret"}},
	"hubic":       {required: []string{"hubic_token"}},
	"swift":       {required: []string{"swift_key"}},
	"webdav":      {required: []string{"webdav_password"}},
	"webdav-http": {required: []string{"webdav_password"}},
	"fabric":      {required: []string{"fabric_token"}},
	"storj":       {required: []string{"storj_key", "storj_passphrase"}},
	"smb":         {required: []string{"smb_password"}},
}

// passwordKeys are the keys of storage.keys that hold the password of the user of a URL,
// by the type of the URL.
var passwordKeys = map[string]string{
	"sftp":        "ssh_password",
	"sftpc":       "ssh_password",
	"webdav":      "webdav_password",
	"webdav-http": "webdav_password",
	"smb":         "smb_password",
}

// storageURL checks storage.url by the forms that duplicacy creates a storage from
// without a network and returns its storage type, fileStorage for a local path, or the
// message of the first failed check: a password in the user information, the local path,
// the type, the form and the parts that the type needs. The messages leave out the URL.
func storageURL(raw string) (typ, msg string) {
	if _, _, ok := passwordSpan(raw); ok {
		scheme, _, _ := strings.Cut(raw, "://")
		if key, ok := passwordKeys[scheme]; ok {
			return "", "must not contain a password, set storage.keys." + key + " instead"
		}
		return "", "must not contain a password"
	}
	if path, ok := localPath(raw); ok {
		switch {
		case !strings.HasPrefix(path, "/"):
			return "", "must have an absolute path after the scheme"
		case strings.Trim(path, "/") == "":
			return "", "must not be the root directory"
		}
		return fileStorage, ""
	}
	if t := storageTypePattern.FindStringSubmatch(raw); t != nil {
		if _, ok := storageTypes[t[1]]; !ok {
			return "", quoted(t[1]) + "is not a storage type"
		}
	}
	m := storageURLPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", "must be an absolute path or a storage URL"
	}
	typ, user, path := m[1], m[2], m[5]
	switch typ {
	case "sftp", "sftpc":
		switch {
		case path == "":
			return "", "must have a path"
		case strings.Trim(path, "/") == "":
			return "", "must not be the root directory"
		}
	case "azure":
		if path == "" {
			return "", "must have a container"
		}
	case "webdav", "webdav-http":
		if user == "" {
			return "", "must have a user"
		}
	case "smb":
		share, _, found := strings.Cut(path, "/")
		switch {
		case user == "":
			return "", "must have a user"
		case !found || share == "":
			return "", "must have a share followed by /"
		}
	case "b2-custom":
		if !b2CustomPattern.MatchString(raw) {
			return "", "must have a bucket"
		}
	}
	return typ, ""
}

// localPath returns the path of a local storage and true: an absolute path as it is or
// the rest after flat:// or samba://, the prefixes as written.
func localPath(raw string) (string, bool) {
	if strings.HasPrefix(raw, "/") {
		return raw, true
	}
	for _, prefix := range []string{"flat://", "samba://"} {
		if rest, ok := strings.CutPrefix(raw, prefix); ok {
			return rest, true
		}
	}
	return "", false
}

// storageKeys checks storage.keys. An empty name and an empty value of a used key are
// required. With the type typ of a valid storage.url, a key that the type does not use,
// password and a name with the prefix of storage.name included, is reported at the key,
// and a missing required key at the nearest present ancestor; without it the names are
// not checked. The paths of the reported keys go to v.rejected, secrets leaves their
// values unchecked.
func (v *validator) storageKeys(typ string) {
	keys := v.l.Config.Storage.Keys
	t := storageTypes[typ]
	for _, name := range slices.Sorted(maps.Keys(keys)) {
		path := joinKey("storage.keys", name)
		switch {
		case name == "":
			v.problem(path, "required")
		case typ == "":
			v.required(path, string(keys[name]))
		case name == "password":
			v.rejected[path] = true
			v.keyProblem(path, "is not a key, set storage.password instead")
		case !slices.Contains(t.required, name) && !slices.Contains(t.optional, name):
			v.rejected[path] = true
			v.keyProblem(path, "is not a key of the "+typ+" storage")
		default:
			v.required(path, string(keys[name]))
		}
	}
	for _, name := range t.required {
		if _, ok := keys[name]; !ok {
			v.problem(joinKey("storage.keys", name), "required")
		}
	}
}
