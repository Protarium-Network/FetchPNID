package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const apiBase = "https://account.protarium.online/v1/api"

var defaultHeaders = map[string]string{
	"Accept":                   "application/json",
	"X-Nintendo-Client-ID":     "a2efa818a34fa16b8afbc8a74eba3eda",
	"X-Nintendo-Client-Secret": "c91cdb5658bd4954ade78533a339cf9a",
}

// Structures for fallback XML parsing
type MappedIDResp struct {
	MappedID struct {
		OutID int64 `xml:"out_id"`
	} `xml:"mapped_id"`
}

type MiiResp struct {
	Mii struct {
		Name string `xml:"name"`
		Data string `xml:"data"`
	} `xml:"mii"`
}

func main() {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mii_data", handleMiiData)
	mux.HandleFunc("/mii_data/", handleMiiData)

	fmt.Printf("API running on http://localhost:%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// setCORS sets standard Cross-Origin Resource Sharing headers
func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Accept")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
}

// fetchPIDFromPNID retrieves the numeric PID from a Pretendo Network ID (PNID) directly from the API
func fetchPIDFromPNID(pnid string) (int64, error) {
	normalized := strings.ToLower(pnid)

	url := fmt.Sprintf("%s/admin/mapped_ids?input_type=user_id&output_type=pid&input=%s", apiBase, normalized)
	respBody, err := makeGetRequest(url)
	if err != nil {
		return 0, err
	}

	// Parse response (supports both JSON and XML fallback)
	var outId int64
	if strings.Contains(string(respBody), "<") {
		var xmlData MappedIDResp
		if err := xml.Unmarshal(respBody, &xmlData); err == nil {
			outId = xmlData.MappedID.OutID
		}
	} else {
		var jsonData map[string]interface{}
		if err := json.Unmarshal(respBody, &jsonData); err == nil {
			if mappedIds, ok := jsonData["mapped_ids"].(map[string]interface{}); ok {
				if mappedId, ok := mappedIds["mapped_id"].(map[string]interface{}); ok {
					if idVal, ok := mappedId["out_id"].(float64); ok {
						outId = int64(idVal)
					}
				}
			}
		}
	}

	if outId == 0 {
		return 0, fmt.Errorf("PNID not found")
	}

	return outId, nil
}

// fetchMii retrieves Mii data for a given PID directly from the API
func fetchMii(pid int64) (map[string]string, error) {
	url := fmt.Sprintf("%s/miis?pids=%d", apiBase, pid)
	respBody, err := makeGetRequest(url)
	if err != nil {
		return nil, err
	}

	mii := make(map[string]string)
	if strings.Contains(string(respBody), "<") {
		var xmlData MiiResp
		if err := xml.Unmarshal(respBody, &xmlData); err == nil {
			mii["name"] = xmlData.Mii.Name
			mii["data"] = xmlData.Mii.Data
		}
	} else {
		var jsonData map[string]interface{}
		if err := json.Unmarshal(respBody, &jsonData); err == nil {
			if miis, ok := jsonData["miis"].(map[string]interface{}); ok {
				if miiObj, ok := miis["mii"].(map[string]interface{}); ok {
					if name, ok := miiObj["name"].(string); ok {
						mii["name"] = name
					}
					if dataVal, ok := miiObj["data"].(string); ok {
						mii["data"] = dataVal
					}
				}
			}
		}
	}

	if mii["name"] == "" && mii["data"] == "" {
		return nil, fmt.Errorf("Mii not found")
	}

	return mii, nil
}

// makeGetRequest performs an HTTP GET request with default custom headers
func makeGetRequest(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// handleMiiData handles incoming HTTP requests for Mii and account details
func handleMiiData(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract PNID from the URL path if present (e.g., /mii_data/my_username)
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mii_data"), "/")
	var pnidFromPath string
	if len(pathParts) > 1 && pathParts[1] != "" {
		pnidFromPath = pathParts[1]
	}

	query := r.URL.Query()
	pnid := pnidFromPath
	if pnid == "" {
		pnid = query.Get("pnid")
	}
	pidQuery := query.Get("pid")

	if pnid == "" && pidQuery == "" {
		http.Error(w, "Missing pnid or pid", http.StatusBadRequest)
		return
	}

	var pid int64
	var err error

	if pidQuery != "" {
		_, err = fmt.Sscanf(pidQuery, "%d", &pid)
		if err != nil {
			http.Error(w, "Invalid PID", http.StatusBadRequest)
			return
		}
	} else {
		pid, err = fetchPIDFromPNID(pnid)
		if err != nil {
			handleError(w, err)
		}
	}

	mii, err := fetchMii(pid)
	if err != nil {
		handleError(w, err)
		return
	}

	acceptHeader := r.Header.Get("Accept")
	formatQuery := query.Get("format")

	// 1. Fetch and stream the PNG image directly from Pretendo CDN if requested
	if acceptHeader == "image/png" || formatQuery == "image" || formatQuery == "png" {
		cdnURL := fmt.Sprintf("https://r2-cdn.pretendo.cc/mii/%d/normal_face.png", pid)
		resp, err := http.Get(cdnURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			// Fallback to standard.png if normal_face.png fails
			cdnURL = fmt.Sprintf("https://r2-cdn.pretendo.cc/mii/%d/standard.png", pid)
			resp, err = http.Get(cdnURL)
			if err != nil || resp.StatusCode != http.StatusOK {
				http.Error(w, "Mii image not found on CDN", http.StatusNotFound)
				return
			}
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, resp.Body)
		return
	}

	// 2. Return raw binary stream if requested via Accept header
	if acceptHeader == "application/octet-stream" {
		w.Header().Set("Content-Type", "application/octet-stream")
		dataStr, _ := mii["data"]
		decoded, _ := base64.StdEncoding.DecodeString(dataStr)
		w.Write(decoded)
		return
	}

	// 3. Default JSON response
	result := map[string]interface{}{
		"name":    mii["name"],
		"pid":     pid,
		"user_id": pnid,
		"data":    mii["data"],
	}

	w.Header().Set("Content-Type", "image/png") // Attente: JSON ici
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleError writes consistent error responses in JSON format
func handleError(w http.ResponseWriter, err error) {
	log.Printf("Error: %v", err)
	if strings.Contains(err.Error(), "not found") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(`{"error":"Internal Server Error"}`))
}
