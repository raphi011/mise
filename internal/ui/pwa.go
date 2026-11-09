package ui

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// ServiceWorker is the service worker script, served at /sw.js so its scope
// is the whole app. It is not content-hashed: browsers look for updates at a
// fixed URL.
//
//go:embed sw.js
var ServiceWorker []byte

// Ground colours of the light and dark themes, for the browser's and the
// installed app's chrome.
const (
	groundLight = "#F6F5F1"
	groundDark  = "#121411"
)

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

// Manifest returns the web app manifest, served at /manifest.webmanifest.
func Manifest() []byte {
	m := struct {
		ID              string         `json:"id"`
		Name            string         `json:"name"`
		ShortName       string         `json:"short_name"`
		StartURL        string         `json:"start_url"`
		Scope           string         `json:"scope"`
		Display         string         `json:"display"`
		BackgroundColor string         `json:"background_color"`
		ThemeColor      string         `json:"theme_color"`
		Icons           []manifestIcon `json:"icons"`
	}{
		ID: "/", Name: "mise", ShortName: "mise", StartURL: "/", Scope: "/",
		Display: "standalone", BackgroundColor: groundLight, ThemeColor: groundLight,
		Icons: []manifestIcon{
			{Src: Asset("img/icon-192.png"), Sizes: "192x192", Type: "image/png"},
			{Src: Asset("img/icon-512.png"), Sizes: "512x512", Type: "image/png"},
			{Src: Asset("img/icon-maskable-512.png"), Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
			{Src: Asset("img/icon.svg"), Sizes: "any", Type: "image/svg+xml"},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("ui: manifest: %v", err))
	}
	return b
}
