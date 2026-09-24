package plugins

import _ "embed"

// Bundled same-key source override. Admin-installed overrides retain their
// existing precedence; the upstream factory file stays independently intact.
//
//go:embed local/doubao.js
var localDoubaoSource string
