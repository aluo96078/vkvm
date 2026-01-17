// Package tray provides system tray functionality using getlantern/systray.
package tray

import (
	"github.com/getlantern/systray"
)

// MenuItem represents a menu item
type MenuItem struct {
	ID       int
	Title    string
	Callback func()
	item     *systray.MenuItem
}

// Tray manages the system tray icon and menu
type Tray struct {
	items   []*MenuItem
	onReady func()
	onExit  func()
	readyCh chan struct{}
	quitCh  chan struct{}
}

// New creates a new system tray
func New(tooltip string) *Tray {
	t := &Tray{
		items:   make([]*MenuItem, 0),
		readyCh: make(chan struct{}),
		quitCh:  make(chan struct{}),
	}

	t.onReady = func() {
		systray.SetTitle("VKVM")
		systray.SetTooltip(tooltip)
		// Use keyboard icon
		systray.SetIcon(getIcon())
		close(t.readyCh)
	}

	t.onExit = func() {
		close(t.quitCh)
	}

	return t
}

// AddMenuItem adds a menu item to the tray
func (t *Tray) AddMenuItem(title string, callback func()) int {
	id := len(t.items)
	menuItem := &MenuItem{
		ID:       id,
		Title:    title,
		Callback: callback,
	}
	t.items = append(t.items, menuItem)
	return id
}

// AddSeparator adds a separator to the menu
func (t *Tray) AddSeparator() {
	t.items = append(t.items, nil) // nil indicates separator
}

// SetItemChecked sets the checked state of a menu item
func (t *Tray) SetItemChecked(id int, checked bool) {
	if id >= 0 && id < len(t.items) && t.items[id] != nil {
		if t.items[id].item != nil {
			if checked {
				t.items[id].item.Check()
			} else {
				t.items[id].item.Uncheck()
			}
		}
	}
}

// Run starts the tray event loop (blocks)
func (t *Tray) Run() {
	systray.Run(t.setupMenu, t.onExit)
}

// setupMenu is called when systray is ready
func (t *Tray) setupMenu() {
	t.onReady()

	// Wait for ready signal
	<-t.readyCh

	// Create menu items
	for _, menuItem := range t.items {
		if menuItem == nil {
			// Separator
			systray.AddSeparator()
		} else {
			item := systray.AddMenuItem(menuItem.Title, "")
			menuItem.item = item

			// Handle clicks in goroutine
			if menuItem.Callback != nil {
				go func(mi *MenuItem) {
					for {
						select {
						case <-mi.item.ClickedCh:
							mi.Callback()
						case <-t.quitCh:
							return
						}
					}
				}(menuItem)
			}
		}
	}
}

// Stop stops the tray
func (t *Tray) Stop() {
	systray.Quit()
}

// getIcon returns a placeholder icon (valid 16x16 ICO)
func getIcon() []byte {
	// A valid 16x16 32-bit ICO file with correct size and DIB header
	icon := make([]byte, 1118)
	// ICO Header
	copy(icon[0:6], []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00})
	// Icon Directory
	copy(icon[6:22], []byte{
		0x10, 0x10, 0x00, 0x00, 0x01, 0x00, 0x20, 0x00,
		0x48, 0x04, 0x00, 0x00, // Size: 1024 (pixels) + 40 (header) + 32 (mask) = 1096 bytes
		0x16, 0x00, 0x00, 0x00, // Offset
	})
	// DIB Header
	copy(icon[22:62], []byte{
		0x28, 0x00, 0x00, 0x00, // Size
		0x10, 0x00, 0x00, 0x00, // Width
		0x20, 0x00, 0x00, 0x00, // Height (16 * 2 for icon)
		0x01, 0x00, // Planes
		0x20, 0x00, // BPP
		0x00, 0x00, 0x00, 0x00, // Compression
		0x00, 0x04, 0x00, 0x00, // Image Size
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	})
	// The rest (pixels and mask) can stay 0 for transparency
	return icon
}
