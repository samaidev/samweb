package breakthrough

import (
        "context"
        "encoding/json"
        "fmt"
        "strings"
        "time"
)

// AliyunBaxiaSlider implements the CDP + JS Teleport technique
// discovered during modelscope.cn login automation.
//
// How it works:
//  1. Detect baxia iframe (#baxia-dialog-content) and slider handle
//     (#nc_1_n1z)
//  2. CDP mousedown on handle (isTrusted=true)
//  3. CDP mousemove × 25 steps (isTrusted=true, handle follows to ~50%)
//  4. JS teleport: set handle.style.left + bg.style.width to 100%
//  5. CDP mouseup (isTrusted=true)
//  6. Check for .nc_ok / .icon_ok (passed) or .errloading (failed)
type AliyunBaxiaSlider struct{}

func (s *AliyunBaxiaSlider) Name() string { return "aliyun-baxia-slider" }

// Detect checks for baxia slider elements in the page or its iframes.
func (s *AliyunBaxiaSlider) Detect(ctx context.Context, env *Env) (bool, map[string]interface{}, error) {
        // Check for baxia iframe
        script := `(function(){
                var iframe = document.getElementById('baxia-dialog-content');
                if (!iframe) return JSON.stringify({found: false});
                try {
                        var doc = iframe.contentDocument;
                        if (!doc) return JSON.stringify({found: false, reason: 'cross-origin'});
                        var handle = doc.querySelector('#nc_1_n1z, .nc_iconfont.btn_slide');
                        var track = doc.querySelector('.nc-lang-cnt, .nc_scale, #nc_1_n1t');
                        var bg = doc.querySelector('#nc_1__bg');
                        if (!handle || !track) return JSON.stringify({found: false, reason: 'no handle/track'});
                        var ir = iframe.getBoundingClientRect();
                        var hr = handle.getBoundingClientRect();
                        var tr = track.getBoundingClientRect();
                        return JSON.stringify({
                                found: true,
                                iframeX: Math.round(ir.left),
                                iframeY: Math.round(ir.top),
                                iframeW: Math.round(ir.width),
                                iframeH: Math.round(ir.height),
                                handleX: Math.round(hr.left + ir.left),
                                handleY: Math.round(hr.top + ir.top + hr.height/2),
                                handleW: Math.round(hr.width),
                                trackW: Math.round(tr.width),
                                trackEndX: Math.round(tr.left + ir.left + tr.width),
                                styleLeft: handle.style.left,
                                bgWidth: bg ? bg.style.width : ''
                        });
                } catch(e) {
                        return JSON.stringify({found: false, reason: e.message});
                }
        })()`

        result, err := env.Eval(ctx, script)
        if err != nil {
                return false, nil, err
        }

        var info map[string]interface{}
        if err := parseEvalResult(result, &info); err != nil {
                return false, nil, err
        }

        if found, ok := info["found"].(bool); ok && found {
                return true, info, nil
        }
        return false, nil, nil
}

// Bypass executes the CDP + JS Teleport technique.
func (s *AliyunBaxiaSlider) Bypass(ctx context.Context, env *Env, meta map[string]interface{}) (bool, error) {
        handleX, _ := meta["handleX"].(float64)
        handleY, _ := meta["handleY"].(float64)
        handleW, _ := meta["handleW"].(float64)
        trackW, _ := meta["trackW"].(float64)
        trackEndX, _ := meta["trackEndX"].(float64)

        if handleW == 0 || trackW == 0 {
                return false, fmt.Errorf("invalid slider dimensions: handleW=%v trackW=%v", handleW, trackW)
        }

        // Step 1: CDP mousedown on handle center (isTrusted=true)
        if err := env.CDPMouse(ctx, "mousePressed", handleX+handleW/2, handleY, "left", 1, 1); err != nil {
                return false, fmt.Errorf("mousedown: %w", err)
        }
        time.Sleep(100 * time.Millisecond)

        // Step 2: CDP mousemove × 25 steps (isTrusted=true)
        // Drag from handle center toward track end, but only to ~50%
        // (baxia limits CDP events to ~50% of track)
        halfX := handleX + (trackEndX-handleX)*0.5
        for i := 1; i <= 25; i++ {
                t := float64(i) / 25.0
                x := handleX + (halfX-handleX)*t
                y := handleY + (float64(i%3)-1) // tiny y jitter
                if err := env.CDPMouse(ctx, "mouseMoved", x, y, "none", 1, 0); err != nil {
                        return false, fmt.Errorf("mousemove %d: %w", i, err)
                }
                time.Sleep(20 * time.Millisecond)
        }

        // Step 3: JS Teleport — set handle.style.left + bg.style.width to 100%
        // Key insight: baxia tracks handle via style.left, not getBoundingClientRect.
        // bg.style.width = handle.style.left + 21 (handle is 42px, half = 21).
        teleportScript := fmt.Sprintf(`(function(){
                var iframe = document.getElementById('baxia-dialog-content');
                if (!iframe) return 'no iframe';
                var d = iframe.contentDocument;
                var w = iframe.contentWindow;
                var h = d.querySelector('#nc_1_n1z');
                var bg = d.querySelector('#nc_1__bg');
                var t = d.querySelector('.nc-lang-cnt, #nc_1_n1t');
                if (!h || !bg || !t) return 'no slider';
                var tr = t.getBoundingClientRect();
                var hr = h.getBoundingClientRect();
                var endLeft = Math.round(tr.width - hr.width);
                var curLeft = parseInt(h.style.left) || 0;
                var sx = hr.left + hr.width/2;
                var sy = hr.top + hr.height/2;
                for (var i = 1; i <= 20; i++) {
                        var x = sx + (tr.width - hr.width - curLeft) * (i/20) + curLeft;
                        h.dispatchEvent(new w.MouseEvent('mousemove', {
                                bubbles: true, cancelable: true,
                                clientX: x, clientY: sy,
                                button: 0, buttons: 1, view: w
                        }));
                }
                h.style.left = endLeft + 'px';
                bg.style.width = (endLeft + 21) + 'px';
                return 'teleported: ' + h.style.left;
        })()`)

        if _, err := env.Eval(ctx, teleportScript); err != nil {
                return false, fmt.Errorf("teleport: %w", err)
        }

        // Step 4: CDP mouseup (isTrusted=true)
        if err := env.CDPMouse(ctx, "mouseReleased", trackEndX, handleY, "left", 0, 1); err != nil {
                return false, fmt.Errorf("mouseup: %w", err)
        }

        // Step 5: Wait and check result
        for w := 1; w <= 12; w++ {
                time.Sleep(time.Duration(w) * time.Second / 2)
                checkScript := `(function(){
                        var iframe = document.getElementById('baxia-dialog-content');
                        if (!iframe) return JSON.stringify({passed: false, failed: false, reason: 'no iframe'});
                        try {
                                var d = iframe.contentDocument;
                                var ok = d.querySelector('.nc_ok, .icon_ok, .nc_iconfont.icon_ok');
                                var err = d.querySelector('.errloading, .nc-lang-cnt .err, .nc_iconfont.btn_warn');
                                return JSON.stringify({passed: !!ok, failed: !!err});
                        } catch(e) {
                                return JSON.stringify({passed: false, failed: false, reason: e.message});
                        }
                })()`

                result, err := env.Eval(ctx, checkScript)
                if err != nil {
                        continue
                }

                var state map[string]interface{}
                if err := parseEvalResult(result, &state); err != nil {
                        continue
                }

                if passed, _ := state["passed"].(bool); passed {
                        return true, nil
                }
                if failed, _ := state["failed"].(bool); failed {
                        return false, fmt.Errorf("baxia verification failed")
                }
        }

        return false, fmt.Errorf("baxia verification timeout")
}

// parseEvalResult extracts the value from the agent API's eval response.
// The response format is: {"value":{"value":"<json-string>"}}
// or sometimes just the raw value.
func parseEvalResult(result string, out interface{}) error {
        result = strings.TrimSpace(result)
        if result == "" {
                return fmt.Errorf("empty result")
        }

        // Try to parse as {"value":{"value":"..."}}
        var wrapper struct {
                Value struct {
                        Value interface{} `json:"value"`
                } `json:"value"`
        }
        if err := json.Unmarshal([]byte(result), &wrapper); err == nil {
                v := wrapper.Value.Value
                // If it's a string, try to parse it as JSON
                if s, ok := v.(string); ok {
                        // Unwrap nested quotes
                        s = strings.TrimSpace(s)
                        for len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
                                s = s[1 : len(s)-1]
                                s = strings.ReplaceAll(s, `\"`, `"`)
                                s = strings.ReplaceAll(s, `\\`, `\`)
                        }
                        return json.Unmarshal([]byte(s), out)
                }
                // If it's already an object, marshal+unmarshal
                if v != nil {
                        b, _ := json.Marshal(v)
                        return json.Unmarshal(b, out)
                }
        }

        // Try direct parse
        return json.Unmarshal([]byte(result), out)
}
