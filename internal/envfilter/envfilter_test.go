package envfilter

import (
	"maps"
	"slices"
	"testing"
)

func TestEnvForward(t *testing.T) {
	const home = "/Users/libraz"
	environ := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/Users/libraz",
		"LANG=ja_JP.UTF-8",
		"CI=true",
		"MAKEFLAGS=-j4",
		"TEST_FILTER=unit",
		"MYAPP_LEVEL=3",
		"MYAPP_TOKEN=t1",
		"GITHUB_TOKEN=g1",
		"db_password=p1",
		"NPM_CONFIG_USERCONFIG=/tmp/npmrc",
		"SSH_AUTH_SOCK=/tmp/agent",
		"TOOL=/opt/homebrew/bin/tool",
		"LIBDIR=/Library/Frameworks",
		"CACHE_DIR=/Users/libraz/.cache",
		"DOTDIR=/Users/libraz/.config/x",
		"WORK_DIR=/Users/libraz/work",
		"TMP_DIR=/var/folders/zz/tmp",
	}
	requested := []string{
		"TEST_FILTER", "MYAPP_*", "GITHUB_TOKEN", "db_*", "NPM_CONFIG_USERCONFIG",
		"SSH_AUTH_SOCK", "TOOL", "LIBDIR", "CACHE_DIR", "DOTDIR", "WORK_DIR", "TMP_DIR", "UNSET_VAR",
	}

	res := Select(environ, home, requested)

	wantEnv := map[string]string{
		"CI":          "true",
		"MAKEFLAGS":   "-j4",
		"TEST_FILTER": "unit",
		"MYAPP_LEVEL": "3",
		"WORK_DIR":    "/Users/libraz/work",
	}
	if !maps.Equal(res.Env, wantEnv) {
		t.Errorf("Env = %v, want %v", res.Env, wantEnv)
	}

	wantDropped := []Dropped{
		{"CACHE_DIR", "Mac-specific value"},
		{"DOTDIR", "Mac-specific value"},
		{"GITHUB_TOKEN", "secret-like name"},
		{"LIBDIR", "Mac-specific value"},
		{"MYAPP_TOKEN", "secret-like name"},
		{"NPM_CONFIG_USERCONFIG", "secret-like name"},
		{"SSH_AUTH_SOCK", "secret-like name"},
		{"TMP_DIR", "Mac-specific value"},
		{"TOOL", "Mac-specific value"},
		{"db_password", "secret-like name"},
	}
	if !slices.Equal(res.Dropped, wantDropped) {
		t.Errorf("Dropped = %v, want %v", res.Dropped, wantDropped)
	}
}
