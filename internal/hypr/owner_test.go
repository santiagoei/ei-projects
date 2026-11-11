package hypr

import "testing"

// The process tree of a herdr client: the window's process is the terminal,
// the client is its child, and a shell sits between them on some setups.
func TestOwnerWalksUpToTheWindow(t *testing.T) {
	ppid := map[int]int{300: 200, 200: 100, 100: 1}
	parent := func(p int) (int, bool) { v, ok := ppid[p]; return v, ok }

	windows := map[int]bool{100: true} // the terminal is the only window
	if got, ok := owner(300, windows, parent); !ok || got != 100 {
		t.Fatalf("client should be owned by the terminal window: %d %v", got, ok)
	}
	// no window above the client: nothing to raise, and no guess
	if _, ok := owner(300, map[int]bool{999: true}, parent); ok {
		t.Fatal("a window that owns nothing here must not be raised")
	}
	// a cycle in the table ends instead of spinning
	loop := func(p int) (int, bool) { return p + 1, true }
	if _, ok := owner(5, map[int]bool{}, loop); ok {
		t.Fatal("a cycle found a window")
	}
}
