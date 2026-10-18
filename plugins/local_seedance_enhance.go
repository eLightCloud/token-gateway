package plugins

import _ "embed"

//go:embed local/seedance_enhance.js
var localSeedanceEnhanceSource string

//go:embed local/seedance_video_storage.js
var localSeedanceVideoStorageSource string
