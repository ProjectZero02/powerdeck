package nanokvm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/nanokvm"
	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
)

func fast(o fakekvm.Options) fakekvm.Options {
	o.TimeScale = 0.001 // 12 s hold = 12 ms
	return o
}

func client(f *fakekvm.Fake, user, pw string) *nanokvm.Client {
	return &nanokvm.Client{Address: f.URL, User: user, Password: pw}
}

func TestLoginStyles(t *testing.T) {
	ctx := context.Background()
	for _, style := range []string{fakekvm.Style25, fakekvm.Style24, fakekvm.StylePro} {
		f := fakekvm.Start(fast(fakekvm.Options{Style: style, Password: "s3cret", On: true}))
		c := client(f, "admin", "s3cret")
		c.Plain = style == fakekvm.StylePro
		on, err := c.PowerLED(ctx)
		if err != nil || !on {
			t.Errorf("%s: led %v %v", style, on, err)
		}
		info, err := c.Info(ctx)
		if err != nil || info.Application == "" || info.Hardware != "Beta" {
			t.Errorf("%s: info %+v %v", style, info, err)
		}
		if f.Logins() != 1 {
			t.Errorf("%s: %d logins for two calls", style, f.Logins())
		}
		f.Close()
	}
}

func TestBadPasswordAndLockout(t *testing.T) {
	f := fakekvm.Start(fast(fakekvm.Options{Password: "right", LockAfter: 3}))
	defer f.Close()
	ctx := context.Background()
	c := client(f, "admin", "wrong")
	for i := 0; i < 2; i++ {
		if err := c.Login(ctx); !errors.Is(err, nanokvm.ErrBadPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := c.Login(ctx); !errors.Is(err, nanokvm.ErrLocked) {
		t.Fatalf("third: %v", err)
	}
	if err := client(f, "admin", "right").Login(ctx); !errors.Is(err, nanokvm.ErrLocked) {
		t.Fatalf("locked for everyone: %v", err)
	}
	// A plain password where the firmware expects the encrypted one is wrong, not a crash.
	f2 := fakekvm.Start(fast(fakekvm.Options{Password: "right"}))
	defer f2.Close()
	pc := client(f2, "admin", "right")
	pc.Plain = true
	if err := pc.Login(ctx); !errors.Is(err, nanokvm.ErrBadPassword) {
		t.Errorf("plain to a 2.5 KVM: %v", err)
	}
}

func TestReloginWhenSessionExpires(t *testing.T) {
	f := fakekvm.Start(fast(fakekvm.Options{On: true}))
	defer f.Close()
	ctx := context.Background()
	c := client(f, "admin", "admin")
	if _, err := c.PowerLED(ctx); err != nil {
		t.Fatal(err)
	}
	f.ExpireSessions()
	if on, err := c.PowerLED(ctx); err != nil || !on {
		t.Fatalf("after expiry: %v %v", on, err)
	}
	if f.Logins() != 2 {
		t.Errorf("logins %d", f.Logins())
	}
}

func TestPressesAndWake(t *testing.T) {
	f := fakekvm.Start(fast(fakekvm.Options{BootDelay: time.Second}))
	defer f.Close()
	ctx := context.Background()
	c := client(f, "admin", "admin")
	if on, _ := c.PowerLED(ctx); on {
		t.Fatal("starts off")
	}
	if err := c.Press(ctx, nanokvm.Power, 800*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if on, _ := c.PowerLED(ctx); !on {
		t.Error("press didn't start it")
	}
	// A 12 s hold: the request stays open for the whole press (scaled here), and power is cut.
	if err := c.Press(ctx, nanokvm.Power, 12*time.Second); err != nil {
		t.Fatal(err)
	}
	if on, _ := c.PowerLED(ctx); on {
		t.Error("hold didn't cut power")
	}
	if err := c.Wake(ctx, "02:00:5E:00:00:0A"); err != nil {
		t.Fatal(err)
	}
	if w := f.Wakes(); len(w) != 1 || w[0] != "02:00:5e:00:00:0a" {
		t.Errorf("wakes %v", w)
	}
	if p := f.Presses(); len(p) != 2 || p[1].Duration != 12*time.Second {
		t.Errorf("presses %+v", p)
	}
	if err := c.Press(ctx, "eject", time.Second); err == nil {
		t.Error("unknown button accepted")
	}
}

func TestHoldOutlastsDefaultTimeout(t *testing.T) {
	// Real time: a 1.5 s press with a 1 s call timeout must still succeed.
	f := fakekvm.Start(fakekvm.Options{On: true})
	defer f.Close()
	c := client(f, "admin", "admin")
	c.Timeout = time.Second
	if err := c.Press(context.Background(), nanokvm.Reset, 1500*time.Millisecond); err != nil {
		t.Fatalf("press: %v", err)
	}
}

func TestFailures(t *testing.T) {
	ctx := context.Background()
	f := fakekvm.Start(fast(fakekvm.Options{Forbidden: true}))
	c := client(f, "admin", "admin")
	if _, err := c.PowerLED(ctx); !errors.Is(err, nanokvm.ErrForbidden) {
		t.Errorf("forbidden: %v", err)
	}
	f.SetUnreachable(true)
	c2 := client(f, "admin", "admin")
	if _, err := c2.PowerLED(ctx); !errors.Is(err, nanokvm.ErrUnreachable) {
		t.Errorf("unreachable: %v", err)
	}
	f.Close()
	if _, err := c2.PowerLED(ctx); !errors.Is(err, nanokvm.ErrUnreachable) {
		t.Errorf("closed: %v", err)
	}
}

func TestHTTPSRedirectAndPin(t *testing.T) {
	f := fakekvm.Start(fast(fakekvm.Options{HTTPS: true, On: true}))
	defer f.Close()
	ctx := context.Background()
	var pinned string
	c := client(f, "admin", "admin")
	c.OnPin = func(p string) { pinned = p }
	if on, err := c.PowerLED(ctx); err != nil || !on {
		t.Fatalf("over https: %v %v", on, err)
	}
	if pinned != f.TLSPin || c.URL() != f.TLSURL {
		t.Errorf("pin %q url %q", pinned, c.URL())
	}
	// Starts on HTTPS next time, with the pin.
	c2 := &nanokvm.Client{Address: c.URL(), User: "admin", Password: "admin", Pin: pinned}
	if _, err := c2.PowerLED(ctx); err != nil {
		t.Errorf("pinned: %v", err)
	}
	c3 := &nanokvm.Client{Address: f.URL, User: "admin", Password: "admin", Pin: "not-the-pin"}
	if _, err := c3.PowerLED(ctx); !errors.Is(err, nanokvm.ErrCertChanged) {
		t.Errorf("changed cert: %v", err)
	}
}

func TestLEDReadFails(t *testing.T) {
	// A failed reading is an error, never "off".
	f := fakekvm.Start(fast(fakekvm.Options{LEDError: true}))
	defer f.Close()
	c := client(f, "admin", "admin")
	var ae *nanokvm.APIError
	if on, err := c.PowerLED(context.Background()); !errors.As(err, &ae) || on {
		t.Fatalf("%v %v", on, err)
	}
}
