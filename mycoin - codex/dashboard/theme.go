//go:build desktopapp
// +build desktopapp

package dashboard

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type customTheme struct {
	fyne.Theme
}

func newDashboardTheme() fyne.Theme {
	return &customTheme{Theme: theme.DefaultTheme()}
}

func (t *customTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	variant = theme.VariantLight

	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 243, G: 244, B: 246, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 31, G: 41, B: 55, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 71, G: 84, B: 229, A: 255}
	case theme.ColorNameSuccess:
		return color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	case theme.ColorNameError:
		return color.NRGBA{R: 239, G: 68, B: 68, A: 255}
	case theme.ColorNameDisabled:
		return color.NRGBA{R: 107, G: 114, B: 128, A: 255}
	}

	return t.Theme.Color(name, variant)
}

func (t *customTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNamePadding {
		return 6
	}
	return t.Theme.Size(name)
}
