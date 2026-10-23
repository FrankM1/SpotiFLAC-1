//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void setAccessoryActivationPolicy(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	});
}
*/
import "C"

// hideFromDock keeps a headless run out of the Dock and the app switcher.
// Wails v2 has no option for this, so it sets the activation policy directly.
func hideFromDock() {
	C.setAccessoryActivationPolicy()
}
