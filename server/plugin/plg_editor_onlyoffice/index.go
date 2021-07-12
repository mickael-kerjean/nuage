package plg_editor_onlyoffice

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	. "github.com/mickael-kerjean/filestash/server/common"
	"github.com/mickael-kerjean/filestash/server/ctrl"
	. "github.com/mickael-kerjean/filestash/server/middleware"
	"github.com/mickael-kerjean/filestash/server/model"
	"github.com/patrickmn/go-cache"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	SECRET_KEY_DERIVATE_FOR_ONLYOFFICE string
	OnlyOfficeCache *cache.Cache
)

type OnlyOfficeCacheData struct {
	Path string
	Save func(path string, file io.Reader) error
	Cat func(path string) (io.ReadCloser, error)
}

func init() {
	plugin_enable := func() bool {
		return Config.Get("features.office.enable").Schema(func(f *FormElement) *FormElement {
			if f == nil {
				f = &FormElement{}
			}
			f.Name = "enable"
			f.Type = "enable"
			f.Target = []string{"onlyoffice_server"}
			f.Description = "Enable/Disable the office suite to manage word, excel and powerpoint documents. This setting requires a restart to comes into effect"
			f.Default = false
			if u := os.Getenv("ONLYOFFICE_URL"); u != "" {
				f.Default = true
			}
			return f
		}).Bool()
	}()
	Config.Get("features.office.onlyoffice_server").Schema(func(f *FormElement) *FormElement {
		if f == nil {
			f = &FormElement{}
		}
		f.Id = "onlyoffice_server"
		f.Name = "onlyoffice_server"
		f.Type = "text"
		f.Description = "Location of your OnlyOffice server"
		f.Default = ""
		f.Placeholder = "Eg: http://127.0.0.1:8080"
		if u := os.Getenv("ONLYOFFICE_URL"); u != "" {
			f.Default = u
			f.Placeholder = fmt.Sprintf("Default: '%s'", u)
		}
		return f
	})

	if plugin_enable == false {
		return
	}

	SECRET_KEY_DERIVATE_FOR_ONLYOFFICE = Hash("ONLYOFFICE_" + SECRET_KEY, len(SECRET_KEY))
	Hooks.Register.HttpEndpoint(func(r *mux.Router, app *App) error {
		oods := r.PathPrefix("/onlyoffice").Subrouter()
		oods.PathPrefix("/static/").HandlerFunc(StaticHandler).Methods("GET", "POST")
		oods.HandleFunc("/event", OnlyOfficeEventHandler).Methods("POST")
		oods.HandleFunc("/content", FetchContentHandler).Methods("GET")

		r.HandleFunc(
			COOKIE_PATH + "onlyoffice/iframe",
			NewMiddlewareChain(
				IframeContentHandler,
				[]Middleware{ SessionStart, LoggedInOnly },
				*app,
			),
		).Methods("GET")
		return nil
	})
	Hooks.Register.XDGOpen(`
    if(mime === "application/word" || mime === "application/msword" ||
       mime === "application/vnd.oasis.opendocument.text" || mime === "application/vnd.oasis.opendocument.spreadsheet" ||
       mime === "application/excel" || mime === "application/vnd.ms-excel" || mime === "application/powerpoint" ||
       mime === "application/vnd.ms-powerpoint" || mime === "application/vnd.oasis.opendocument.presentation" ) {
              return ["appframe", {"endpoint": "/api/onlyoffice/iframe"}];
       }
    `)
	OnlyOfficeCache = cache.New(720*time.Minute, 720*time.Minute)
}

func StaticHandler(res http.ResponseWriter, req *http.Request) {
	req.URL.Path = strings.TrimPrefix(req.URL.Path, "/onlyoffice/static")
	oodsLocation := Config.Get("features.office.onlyoffice_server").String()
	u, err := url.Parse(oodsLocation)
	if err != nil {
		SendErrorResult(res, err)
		return
	}
	req.Header.Set("X-Forwarded-Host", req.Host + "/onlyoffice/static")
	req.Header.Set("X-Forwarded-Proto", func() string {
		if scheme := req.Header.Get("X-Forwarded-Proto"); scheme != "" {
			return scheme
		} else if req.TLS != nil {
			return "https"
		}
		return "http"
	}())

	// This code is a copy and paste from httputil.NewSingleHostReverseProxy with 1 single change
	// to do SSL termination.
	reverseProxy := &httputil.ReverseProxy{
		Director: func(rq *http.Request) {
			rq.URL.Scheme = "http" // <- this is the only change from NewSingleHostReverseProxy
			rq.URL.Host = u.Host
			rq.URL.Path = func(a, b string) string {
				aslash := strings.HasSuffix(a, "/")
				bslash := strings.HasPrefix(b, "/")
				switch {
				case aslash && bslash:
					return a + b[1:]
				case !aslash && !bslash:
					return a + "/" + b
				}
				return a + b
			}(u.Path, rq.URL.Path)
			if u.RawQuery == "" || rq.URL.RawQuery == "" {
				rq.URL.RawQuery = u.RawQuery + rq.URL.RawQuery
			} else {
				rq.URL.RawQuery = u.RawQuery + "&" + rq.URL.RawQuery
			}
		},
	}
	reverseProxy.ErrorHandler = func(rw http.ResponseWriter, rq *http.Request, err error) {
		Log.Warning("[onlyoffice] %s", err.Error())
		SendErrorResult(rw, NewError(err.Error(), http.StatusBadGateway))
	}
	reverseProxy.ServeHTTP(res, req)
}

func IframeContentHandler(ctx App, res http.ResponseWriter, req *http.Request) {
	if model.CanRead(&ctx) == false {
		SendErrorResult(res, ErrPermissionDenied)
		return
	} else if oodsLocation := Config.Get("features.office.onlyoffice_server").String(); oodsLocation == "" {
		res.WriteHeader(http.StatusServiceUnavailable)
		res.Write([]byte("<p>The Onlyoffice server hasn't been configured</p>"))
		res.Write([]byte("<style>p {color: white; text-align: center; margin-top: 50px; font-size: 20px; opacity: 0.6; font-family: monospace; } </style>"))
		return
	}

	var (
		path string                     // path of the file we want to open via onlyoffice
		filestashServerLocation string  // location from which the oods server can reach filestash
		userId string                   // as seen by onlyoffice to distinguish different users
		username string                 // username as displayed by only office
		key string                      // unique identifier for a file as seen be only office
		contentType string              // name of the application in onlyoffice
		filetype string                 // extension of the document
		filename string                 // filename of the document
		oodsMode string                 // edit mode
		oodsDevice string               // mobile, desktop of embedded
		localip string
		lang string
	)
	query := req.URL.Query()
	path, err := ctrl.PathBuilder(ctx, query.Get("path"))
	if err != nil {
		SendErrorResult(res, err)
		return
	}

	userId = GenerateID(&ctx)
	f, err := ctx.Backend.Cat(path)
	if err != nil {
		SendErrorResult(res, err)
		return
	}
	key = HashStream(f, 20)
	key = Hash(key + path, 20)

	filename = filepath.Base(path)
	oodsMode = func() string {
		if model.CanEdit(&ctx) == false {
			return "view"
		}
		return "edit"
	}()
	oodsDevice = func() string {
		ua := req.Header.Get("User-Agent")
		if ua == "" {
			return "desktop"
		} else if strings.Contains(ua, "iPhone") {
			return "mobile"
		} else if strings.Contains(ua, "iPad") {
			return "mobile"
		} else if strings.Contains(ua, "Android") {
			return "mobile"
		} else if strings.Contains(ua, "Mobile") {
			return "mobile"
		}

		if oodsMode == "view" {
			return "embedded"
		}
		return "desktop"
	}()
	username = func() string {
		if ctx.Session["username"] != "" {
			return ctx.Session["username"]
		}
		return "Me"
	}()
	if ctx.Share.Id != "" {
		username = "Anonymous"
		userId = RandomString(10)
	}
	localip = func() string { // https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go#23558495
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return ""
		}
		for _, address := range addrs {
			if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String()
				}
			}
		}
		return ""
	}()
	lang = func() string {
		lang := req.Header.Get("Accept-Language")
		if lang == "" {
			return "en"
		}

		return lang[0:2]
	}()
	filestashServerLocation = fmt.Sprintf("http://%s:%d", localip, Config.Get("general.port").Int())
	contentType = func(p string) string {
		var (
			word string = "text"
			excel string = "spreadsheet"
			powerpoint string = "presentation"
		)
		switch GetMimeType(p) {
		case "application/word": return word
		case "application/msword": return word
		case "application/vnd.oasis.opendocument.text": return word
		case "application/vnd.oasis.opendocument.spreadsheet": return excel
		case "application/excel": return excel
		case "application/vnd.ms-excel": return excel
		case "application/powerpoint": return powerpoint
		case "application/vnd.ms-powerpoint": return powerpoint
		case "application/vnd.oasis.opendocument.presentation": return powerpoint
		}
		return ""
	}(path)
	filetype = strings.TrimPrefix(filepath.Ext(filename), ".")
	OnlyOfficeCache.Set(key, &OnlyOfficeCacheData{ path, ctx.Backend.Save, ctx.Backend.Cat }, cache.DefaultExpiration)
	res.Write([]byte(fmt.Sprintf(`<!DOCTYPE html>
<html lang="%s">
  <head>
    <meta charset="utf-8">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
   <meta name="viewport" content="width=device-width, initial-scale=1">
  </head>
  <body>
    <style> body { margin: 0; } body, html{ height: 100%%; } iframe { width: 100%%; height: 100%%; } </style>
    <div id="placeholder"></div>
    <script type="text/javascript" src="/onlyoffice/static/web-apps/apps/api/documents/api.js"></script>
    <script>
      if("DocsAPI" in window) loadApplication();
      else sendError("[error] Can't reach the onlyoffice server");

      function loadApplication() {
          new DocsAPI.DocEditor("placeholder", {
              "token": "foobar",
              "documentType": "%s",
              "type": "%s",
              "document": {
                  "title": "%s",
                  "url": "%s/onlyoffice/content?key=%s",
                  "fileType": "%s",
                  "key": "%s"
              },
              "editorConfig": {
                  "callbackUrl": "%s/onlyoffice/event",
                  "mode": "%s",
                  "lang": "%s",
                  "customization": {
                      "autosave": false,
                      "forcesave": true,
                      "compactHeader": true
                  },
                  "user": {
                      "id": "%s",
                      "name": "%s"
                  }
              }
          });
      }
      function sendError(message){
          let $el = document.createElement("p");
          $el.innerHTML = message;
          $el.setAttribute("style", "text-align: center; color: white; opacity: 0.8; font-size: 20px; font-family: monospace;");
          document.body.appendChild($el);
      }
    </script>
  </body>
</html>`,
        lang,
		contentType,
		oodsDevice,
		filename,
		filestashServerLocation, key,
		filetype,
		key,
		filestashServerLocation,
		oodsMode,
		lang,
		userId,
		username,
	)))
}

func FetchContentHandler(res http.ResponseWriter, req *http.Request) {
	var key string
	if key = req.URL.Query().Get("key"); key == "" {
		SendErrorResult(res, NewError("unspecified key", http.StatusBadRequest))
		return
	}
	c, found := OnlyOfficeCache.Get(key)
	if found == false {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(`{"error": 1, "message": "missing data fetcher handler"}`))
		return
	}
	cData, valid := c.(*OnlyOfficeCacheData)
	if valid == false {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(`{"error": 1, "message": "invalid cache"}`))
		return
	}
	f, err := cData.Cat(cData.Path)
	if err != nil {
		res.WriteHeader(http.StatusNotFound)
		res.Write([]byte(`{"error": 1, "message": "error while fetching data"}`))
		return
	}
	io.Copy(res, f)
	f.Close()
}

type OnlyOfficeEventObject struct {
	Actions []struct {
		Type   int           `json: "type"`
		UserId string        `json: "userid" `
	}                        `json: "actions"`
	ChangesURL string        `json: "changesurl"`
	Forcesavetype int        `json: "forcesavetype"`
	History struct {
		ServerVersion string `json: "serverVersion"`
		Changes []struct {
			Created string   `json: "created"`
			User struct {
				Id   string  `json: "id"`
				Name string  `json: "name"`
			}
		}                    `json: "changes"`
	}                        `json: "history"`
	Key      string          `json: "key"`
	Status   int             `json: "status"`
	Url      string          `json: "url"`
	UserData string          `json: "userdata"`
	Lastsave string          `json: "lastsave"`
	Users    []string        `json: "users"`
}

func OnlyOfficeEventHandler(res http.ResponseWriter, req *http.Request) {
	event := OnlyOfficeEventObject{}
	if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
		SendErrorResult(res, err)
        return
    }
	req.Body.Close()

	switch event.Status {
	case 0: Log.Warning("[onlyoffice] no document with the key identifier could be found. %+v", event)
	case 1:
		// document is being edited
	case 2:
		// document is ready for saving
	case 3:
		// document saving error has occurred
		Log.Warning("[onlyoffice] document saving error has occurred. %+v", event)
	case 4:
		// document is closed with no changes
	case 5:
		Log.Warning("[onlyoffice] undocumented status. %+v", event)
	case 6: // document is being edited, but the current document state is saved
		saveObject, found := OnlyOfficeCache.Get(event.Key);
		if found == false {
			res.WriteHeader(http.StatusInternalServerError)
			res.Write([]byte(`{"error": 1, "message": "doens't know where to store the given data"}`))
			return
		}
		cData, valid := saveObject.(*OnlyOfficeCacheData)
		if valid == false {
			res.WriteHeader(http.StatusInternalServerError)
			res.Write([]byte(`{"error": 1, "message": "[internal error] invalid save handler"}`))
			return
		}

		r, err := http.NewRequest("GET", event.Url, nil)
		if err != nil {
			res.WriteHeader(http.StatusInternalServerError)
			res.Write([]byte(`{"error": 1, "message": "couldn't fetch the document on the oods server"}`))
			return
		}
		f, err := HTTPClient.Do(r)
		if err = cData.Save(cData.Path, f.Body); err != nil {
			res.WriteHeader(http.StatusInternalServerError)
			res.Write([]byte(`{"error": 1, "message": "error while saving the document"}`))
			return
		}
		f.Body.Close()
	case 7: Log.Warning("[onlyoffice] error has occurred while force saving the document. %+v", event)
	default: Log.Warning("[onlyoffice] undocumented status. %+v", event)
	}
	res.Write([]byte(`{"error": 0}`))
}
