package browser

import (
        "encoding/base64"
        "encoding/json"
        "net/http"
)

// base64Decode wraps base64.StdDecodeString.
func base64Decode(s string) ([]byte, error) {
        return base64.StdDecodeString(s)
}

// HandleCallbackHTTP is an http.HandlerFunc that receives JS callbacks
// from the /agent/callback endpoint. It parses the JSON body and
// forwards the result to the WailsBackend's HandleCallback method.
func HandleCallbackHTTP(backend *WailsBackend) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
                if r.Method != http.MethodPost {
                        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
                        return
                }
                var body struct {
                        ID     string `json:"id"`
                        Result string `json:"result"`
                        Error  string `json:"error"`
                }
                if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
                        http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
                        return
                }
                backend.HandleCallback(body.ID, body.Result, body.Error)
                w.WriteHeader(http.StatusOK)
        }
}
