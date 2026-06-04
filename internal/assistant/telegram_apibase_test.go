// SPDX-License-Identifier: GPL-3.0-only

package assistant

import "testing"

func TestApplyTelegramAPIBase(t *testing.T) {
	origAPI, origFile := telegramAPIBaseURL, telegramFileAPIBaseURL
	t.Cleanup(func() {
		telegramAPIBaseURL = origAPI
		telegramFileAPIBaseURL = origFile
	})

	t.Run("empty keeps defaults", func(t *testing.T) {
		telegramAPIBaseURL, telegramFileAPIBaseURL = telegramAPIBase, telegramFileAPIBase
		applyTelegramAPIBase("")
		if telegramAPIBaseURL != telegramAPIBase || telegramFileAPIBaseURL != telegramFileAPIBase {
			t.Fatalf("empty base must keep defaults, got %q / %q", telegramAPIBaseURL, telegramFileAPIBaseURL)
		}
	})

	t.Run("root override appends bot and file suffixes", func(t *testing.T) {
		telegramAPIBaseURL, telegramFileAPIBaseURL = telegramAPIBase, telegramFileAPIBase
		applyTelegramAPIBase("http://gateway:8090")
		if got, want := telegramAPIBaseURL, "http://gateway:8090/bot"; got != want {
			t.Fatalf("bot base = %q, want %q", got, want)
		}
		if got, want := telegramFileAPIBaseURL, "http://gateway:8090/file/bot"; got != want {
			t.Fatalf("file base = %q, want %q", got, want)
		}
	})

	t.Run("trailing slash and whitespace trimmed", func(t *testing.T) {
		telegramAPIBaseURL, telegramFileAPIBaseURL = telegramAPIBase, telegramFileAPIBase
		applyTelegramAPIBase("  http://gateway:8090/  ")
		if got, want := telegramAPIBaseURL, "http://gateway:8090/bot"; got != want {
			t.Fatalf("bot base = %q, want %q", got, want)
		}
	})
}
