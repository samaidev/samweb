package agent

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "image"
        "image/color"
        "image/png"
        "sync"
        "time"
)

// MockBackend is an in-memory Backend implementation used for testing the
// Agent server without spinning up a real webview. It simulates a tiny
// fake browser with a small fake DOM so the API contract can be exercised
// end-to-end.
//
// The fake DOM is intentionally simple: it has a fixed list of elements
// (a heading, two links, an input box, a button) and tracks which element
// was last clicked, what text was typed, the current URL, and the
// navigation history (for Back/Forward).
type MockBackend struct {
        mu sync.Mutex

        currentURL string
        title      string
        history    []string
        histIdx    int

        typedText  map[string]string // selector -> text typed into it
        lastClick  string            // selector of last-clicked element
        lastScroll struct {
                X, Y float64
        }
        keyLog []string

        elements []mockEl
}

type mockEl struct {
        Tag     string
        ID      string
        Classes []string
        Text    string
        Attrs   map[string]string
        // position
        X, Y, W, H float64
}

// NewMockBackend returns a MockBackend preloaded with a fake page that
// looks vaguely like a search results page, so tests have something to
// interact with.
func NewMockBackend() *MockBackend {
        m := &MockBackend{
                typedText: map[string]string{},
        }
        m.loadFakePage("https://example.com/search?q=hello", "Example Search")
        // Seed the history with the initial page so Back/Forward have a baseline.
        m.history = []string{m.currentURL}
        m.histIdx = 0
        return m
}

// loadFakePage populates the mock with a fake page resembling a search
// results page. Used both at construction and on every Navigate. It does
// NOT touch history; the caller is responsible for that.
func (m *MockBackend) loadFakePage(url, title string) {
        m.currentURL = url
        m.title = title
        m.elements = []mockEl{
                {Tag: "h1", ID: "title", Text: title, X: 40, Y: 40, W: 400, H: 40, Attrs: map[string]string{"id": "title"}},
                {Tag: "input", ID: "searchbox", Classes: []string{"search"}, Text: "", X: 40, Y: 100, W: 600, H: 36, Attrs: map[string]string{"id": "searchbox", "type": "text", "placeholder": "Search..."}},
                {Tag: "a", ID: "link1", Classes: []string{"result"}, Text: "Hello World", X: 40, Y: 180, W: 200, H: 20, Attrs: map[string]string{"id": "link1", "href": "https://hello.world"}},
                {Tag: "a", ID: "link2", Classes: []string{"result"}, Text: "Hello GitHub", X: 40, Y: 210, W: 200, H: 20, Attrs: map[string]string{"id": "link2", "href": "https://github.com"}},
                {Tag: "button", ID: "submit", Classes: []string{"primary"}, Text: "Search", X: 660, Y: 100, W: 80, H: 36, Attrs: map[string]string{"id": "submit", "type": "submit"}},
                {Tag: "div", ID: "content", Classes: []string{"main"}, Text: "Welcome to the mock page. This is sample content for testing the agent API.", X: 40, Y: 280, W: 700, H: 200, Attrs: map[string]string{"id": "content"}},
        }
}

// ----------------------------- Backend impl -----------------------------

func (m *MockBackend) Navigate(ctx context.Context, url string) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        // Trim forward history if navigating from the middle.
        m.history = m.history[:m.histIdx+1]
        m.history = append(m.history, url)
        m.histIdx = len(m.history) - 1
        m.loadFakePage(url, "Example Search: "+url)
        return nil
}

func (m *MockBackend) NavigateDirect(ctx context.Context, url string) error {
        return m.Navigate(ctx, url)
}

func (m *MockBackend) Back(ctx context.Context) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        if m.histIdx <= 0 {
                return fmt.Errorf("no back history")
        }
        m.histIdx--
        m.loadFakePage(m.history[m.histIdx], "Example Search: "+m.history[m.histIdx])
        return nil
}

func (m *MockBackend) Forward(ctx context.Context) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        if m.histIdx >= len(m.history)-1 {
                return fmt.Errorf("no forward history")
        }
        m.histIdx++
        m.loadFakePage(m.history[m.histIdx], "Example Search: "+m.history[m.histIdx])
        return nil
}

func (m *MockBackend) Reload(ctx context.Context) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.loadFakePage(m.currentURL, m.title)
        return nil
}

func (m *MockBackend) Stop(ctx context.Context) error {
        return nil
}

func (m *MockBackend) Click(ctx context.Context, opts ClickOpts) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        if opts.Selector != "" {
                el := m.findBySelectorLocked(opts.Selector)
                if el == nil {
                        return fmt.Errorf("element not found: %s", opts.Selector)
                }
                m.lastClick = opts.Selector
                return nil
        }
        if opts.X != nil && opts.Y != nil {
                el := m.findAtPointLocked(*opts.X, *opts.Y)
                if el == nil {
                        return fmt.Errorf("no element at (%.0f, %.0f)", *opts.X, *opts.Y)
                }
                m.lastClick = el.ID
                return nil
        }
        return fmt.Errorf("click requires selector or x,y")
}

// LastClick returns the selector (or element ID) of the element most
// recently clicked via Click. It is intended for test assertions.
func (m *MockBackend) LastClick() string {
        m.mu.Lock()
        defer m.mu.Unlock()
        return m.lastClick
}

// ResetCookies is a no-op on the mock backend, which has no cookie jar.
// It exists so the Backend interface can include a ResetCookies method
// without forcing every implementation to be aware of it.
func (m *MockBackend) ResetCookies(ctx context.Context) error { return nil }

// SaveCookies is a no-op on the mock backend.
func (m *MockBackend) SaveCookies(ctx context.Context) error { return nil }

// LoadCookies is a no-op on the mock backend.
func (m *MockBackend) LoadCookies(ctx context.Context) error { return nil }

func (m *MockBackend) Scroll(ctx context.Context, opts ScrollOpts) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        switch {
        case opts.X != nil && opts.Y != nil:
                m.lastScroll.X = *opts.X
                m.lastScroll.Y = *opts.Y
        case opts.Direction != "":
                amount := opts.Amount
                if amount == 0 {
                        amount = 400
                }
                switch opts.Direction {
                case "down":
                        m.lastScroll.Y += float64(amount)
                case "up":
                        m.lastScroll.Y -= float64(amount)
                case "right":
                        m.lastScroll.X += float64(amount)
                case "left":
                        m.lastScroll.X -= float64(amount)
                }
        case opts.Selector != "":
                el := m.findBySelectorLocked(opts.Selector)
                if el == nil {
                        return fmt.Errorf("element not found: %s", opts.Selector)
                }
                m.lastScroll.Y = el.Y
        default:
                return fmt.Errorf("scroll requires either x,y or direction or selector")
        }
        return nil
}

func (m *MockBackend) Type(ctx context.Context, opts TypeOpts) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        var sel string
        if opts.Selector != "" {
                sel = opts.Selector
        } else if opts.X != nil && opts.Y != nil {
                el := m.findAtPointLocked(*opts.X, *opts.Y)
                if el == nil {
                        return fmt.Errorf("no element at (%.0f, %.0f)", *opts.X, *opts.Y)
                }
                sel = el.ID
        } else {
                return fmt.Errorf("type requires selector or x,y")
        }
        if opts.Clear {
                m.typedText[sel] = opts.Text
        } else {
                m.typedText[sel] = m.typedText[sel] + opts.Text
        }
        return nil
}

func (m *MockBackend) PressKey(ctx context.Context, opts KeyOpts) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.keyLog = append(m.keyLog, opts.Key)
        return nil
}

// Drag is a no-op on the mock backend (it has no real DOM to dispatch
// mouse events to). The real WebviewBackend dispatches the drag via
// the agent JS bridge.
func (m *MockBackend) Drag(ctx context.Context, opts DragOpts) error {
        return nil
}

func (m *MockBackend) Eval(ctx context.Context, script string) (json.RawMessage, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        // Simulate a tiny expression evaluator so tests can verify round-tripping.
        switch script {
        case "1 + 1":
                return json.RawMessage("2"), nil
        case "document.title":
                return json.RawMessage(jsonQuote(m.title)), nil
        case "window.location.href":
                return json.RawMessage(jsonQuote(m.currentURL)), nil
        case "document.querySelectorAll('a').length":
                count := 0
                for _, el := range m.elements {
                        if el.Tag == "a" {
                                count++
                        }
                }
                return json.RawMessage(fmt.Sprintf("%d", count)), nil
        }
        // Treat any other script as returning its own source as a string (so
        // tests can verify arbitrary scripts were received and executed).
        return json.RawMessage(jsonQuote("evaluated: " + script)), nil
}

func (m *MockBackend) Wait(ctx context.Context, selector string, timeoutMs int) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        if m.findBySelectorLocked(selector) != nil {
                return nil
        }
        // Simulate a timeout. If timeoutMs is 0 use 100ms so tests don't hang.
        d := time.Duration(timeoutMs) * time.Millisecond
        if d == 0 {
                d = 100 * time.Millisecond
        }
        time.Sleep(d)
        if m.findBySelectorLocked(selector) != nil {
                return nil
        }
        return fmt.Errorf("timeout waiting for %s", selector)
}

func (m *MockBackend) Elements(ctx context.Context, selector string) ([]Element, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        var out []Element
        for _, el := range m.elements {
                if m.matchesSelectorLocked(el, selector) {
                        out = append(out, el.toElement())
                }
        }
        return out, nil
}

func (m *MockBackend) Element(ctx context.Context, selector string) (*Element, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        el := m.findBySelectorLocked(selector)
        if el == nil {
                return nil, fmt.Errorf("element not found: %s", selector)
        }
        e := el.toElement()
        return &e, nil
}

func (m *MockBackend) State(ctx context.Context) (*State, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        return &State{
                URL:        m.currentURL,
                Title:      m.title,
                ActiveTab:  1,
                CanBack:    m.histIdx > 0,
                CanForward: m.histIdx < len(m.history)-1,
                Tabs: []TabInfo{
                        {ID: 1, Title: m.title, URL: m.currentURL},
                },
        }, nil
}

func (m *MockBackend) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
        m.mu.Lock()
        defer m.mu.Unlock()
        w, h := 800, 600
        if fullPage {
                h = 1200
        }
        img := image.NewRGBA(image.Rect(0, 0, w, h))
        // White background
        for y := 0; y < h; y++ {
                for x := 0; x < w; x++ {
                        img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
                }
        }
        // Draw a few colored bars to fake content
        for _, el := range m.elements {
                // Light blue rectangle for each element
                c := color.RGBA{R: 200, G: 220, B: 255, A: 255}
                for y := int(el.Y); y < int(el.Y+el.H) && y < h; y++ {
                        for x := int(el.X); x < int(el.X+el.W) && x < w; x++ {
                                img.Set(x, y, c)
                        }
                }
        }
        var buf bytes.Buffer
        if err := png.Encode(&buf, img); err != nil {
                return nil, err
        }
        return buf.Bytes(), nil
}

func (m *MockBackend) Close() error { return nil }

// ----------------------------- helpers -----------------------------

func (m *MockBackend) findBySelectorLocked(selector string) *mockEl {
        for i := range m.elements {
                if m.matchesSelectorLocked(m.elements[i], selector) {
                        return &m.elements[i]
                }
        }
        return nil
}

func (m *MockBackend) findAtPointLocked(x, y float64) *mockEl {
        for i := range m.elements {
                el := m.elements[i]
                if x >= el.X && x <= el.X+el.W && y >= el.Y && y <= el.Y+el.H {
                        return &m.elements[i]
                }
        }
        return nil
}

// matchesSelectorLocked supports a tiny subset of CSS selectors: tag name,
// #id, .class, and the * wildcard. Combined selectors like "a.result" also
// work. Anything more complex returns false (no match).
func (m *MockBackend) matchesSelectorLocked(el mockEl, selector string) bool {
        if selector == "*" {
                return true
        }
        // Split on '.' for class, '#' for id, leading tag.
        parts := splitSelector(selector)
        if parts.tag != "" && parts.tag != el.Tag {
                return false
        }
        if parts.id != "" && parts.id != el.ID {
                return false
        }
        if parts.cls != "" {
                found := false
                for _, c := range el.Classes {
                        if c == parts.cls {
                                found = true
                                break
                        }
                }
                if !found {
                        return false
                }
        }
        return true
}

type selectorParts struct{ tag, id, cls string }

func splitSelector(s string) selectorParts {
        var p selectorParts
        i := 0
        for i < len(s) {
                switch s[i] {
                case '#':
                        j := i + 1
                        for j < len(s) && s[j] != '.' && s[j] != '#' {
                                j++
                        }
                        p.id = s[i+1 : j]
                        i = j
                case '.':
                        j := i + 1
                        for j < len(s) && s[j] != '.' && s[j] != '#' {
                                j++
                        }
                        p.cls = s[i+1 : j]
                        i = j
                default:
                        j := i
                        for j < len(s) && s[j] != '.' && s[j] != '#' {
                                j++
                        }
                        p.tag = s[i:j]
                        i = j
                }
        }
        return p
}

func (e mockEl) toElement() Element {
        out := Element{
                Tag:     e.Tag,
                ID:      e.ID,
                Classes: e.Classes,
                Text:    e.Text,
                Attrs:   e.Attrs,
                X:       e.X,
                Y:       e.Y,
                Width:   e.W,
                Height:  e.H,
        }
        if out.Attrs == nil {
                out.Attrs = map[string]string{}
        }
        if e.ID != "" {
                out.Attrs["id"] = e.ID
        }
        return out
}

// jsonQuote returns the JSON string encoding of s.
func jsonQuote(s string) string {
        b, _ := json.Marshal(s)
        return string(b)
}
