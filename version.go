package main

// version is the release tag this binary was built from. CI overwrites this
// file with the pushed tag on tag builds; branch/PR builds and local builds
// keep "dev", which never prompts for updates.
//
// This file must stay stamp-only: the tag-build step in ci.yml replaces its
// entire contents. Anything else (parseSemver, updateAvailable, …) belongs
// in semver.go.
var version = "dev"
