// -*- tab-width: 4 -*-

// CouchDB API
package couch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

var (
	defaultHeaders = map[string][]string{}
)

// getURL performs a HTTP GET against the URL u
// and returns the response body as a ReadCloser.
func getURL(u string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	urlObj, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	if urlObj.User != nil {
		if password, ok := urlObj.User.Password(); ok {
			req.SetBasicAuth(urlObj.User.Username(), password)
		}
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != 200 {
		return nil, fmt.Errorf(r.Status)
	}
	return r.Body, nil
}

// decodeJSON decodes the JSON data in buf to the passed interface.
func decodeJSON(r io.Reader, d interface{}) error {
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(d); err != nil {
		return err
	}
	return nil
}

// unmarshalUrl makes a HTTP GET against the URL u, and unmarshals
// the (presumed) JSON response into the given results.
func unmarshalURL(u string, results interface{}) error {
	r, err := getURL(u)
	if err != nil {
		return err
	}
	defer r.Close()
	return decodeJSON(r, results)
}

type IdAndRev struct {
	Id  string `json:"_id"`
	Rev string `json:"_rev"`
}

// interact queries CouchDB and parses the response.
// method: the name of the HTTP method (POST, PUT, ...)
// url: the URL to interact with
// headers: additional headers to pass to the request
// in: body of the request
// out: a structure to fill in with the returned JSON document
func (p Database) interact(method, u string, headers map[string][]string, in []byte, out interface{}) (int, error) {
	bodyLength := 0
	if in != nil {
		bodyLength = len(in)
		headers["Content-Type"] = []string{"application/json"}
	}
	req := http.Request{
		Method:        method,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Close:         true,
		ContentLength: int64(bodyLength),
		Header:        headers,
	}
	req.TransferEncoding = []string{"chunked"}
	var err error
	req.URL, err = url.Parse(u)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Body = ioutil.NopCloser(bytes.NewBuffer(in))
	}
	if req.URL.User != nil {
		if password, ok := req.URL.User.Password(); ok {
			req.SetBasicAuth(req.URL.User.Username(), password)
		}
	}
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%s", p.Host, p.Port))
	if err != nil {
		return 0, err
	}
	httpConn := httputil.NewClientConn(conn, nil)
	defer httpConn.Close()
	if err := httpConn.Write(&req); err != nil {
		return 0, err
	}
	r, err := httpConn.Read(&req)
	if err != nil && err != httputil.ErrPersistEOF {
		return 0, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		b := []byte{}
		r.Body.Read(b)
		return r.StatusCode, fmt.Errorf(r.Status)
	}
	decoder := json.NewDecoder(r.Body)
	if err = decoder.Decode(out); err != nil && err != httputil.ErrPersistEOF {
		return 0, err
	}
	return r.StatusCode, nil
}

type Database struct {
	Host string
	Port string
	Name string
	Auth *url.Userinfo
}

func (p Database) BaseURL() string {
	authStr := ""
	if p.Auth != nil {
		authStr = fmt.Sprintf("%s@", p.Auth.String())
	}
	return fmt.Sprintf("http://%s%s:%s", authStr, p.Host, p.Port)
}

func (p Database) DBURL() string {
	return fmt.Sprintf("%s/%s", p.BaseURL(), p.Name)
}

// Example: couch.NewDatabase("localhost", "5984", "testdb")
// Note: if you want authentication, use NewDatabaseByURL().
func NewDatabase(host, port, name string) (Database, error) {
	return NewDatabaseByURL(fmt.Sprintf("http://%s:%s/%s", host, port, name))
}

// Example: couch.NewDatabaseByURL("http://user:pass@localhost:5984/testdb/")
func NewDatabaseByURL(dburl string) (Database, error) {
	u, err := url.Parse(dburl)
	if err != nil {
		return Database{}, err
	}
	host, port := u.Host, "5984"
	if toks := strings.Split(u.Host, ":"); len(toks) > 1 {
		host, port = toks[0], toks[1]
	}
	db := Database{host, port, u.Path[1:], u.User}
	if err = db.ensureDatabase(); err != nil {
		return Database{}, err
	}
	return db, nil
}

func (p Database) ensureDatabase() error {
	if !p.Running() {
		return fmt.Errorf("CouchDB not running")
	}
	if !p.Exists() {
		if err := p.createDatabase(); err != nil {
			return err
		}
	}
	return nil
}

// Test whether CouchDB is running (ignores Database.Name)
func (p Database) Running() bool {
	dbs := []string{}
	u := fmt.Sprintf("%s/%s", p.BaseURL(), "_all_dbs")
	if err := unmarshalURL(u, &dbs); err != nil {
		return false
	}
	if len(dbs) > 0 {
		return true
	}
	return false
}

// Test whether specified database exists in specified CouchDB instance
func (p Database) Exists() bool {
	di := &databaseInfo{}
	if err := unmarshalURL(p.DBURL(), &di); err != nil {
		return false
	}
	if di.Name != p.Name {
		return false
	}
	return true
}

// Deletes the given database and all documents
func (p Database) DeleteDatabase() error {
	r := couchResponse{}
	if _, err := p.interact("DELETE", p.DBURL(), defaultHeaders, nil, &r); err != nil {
		return err
	}
	if !r.Ok {
		return fmt.Errorf("Delete database operation returned not-OK")
	}
	return nil
}

// Inserts document to CouchDB, returning id and rev on success.
// Document may specify both "_id" and "_rev" fields (will overwrite existing)
// or just "_id" (will use that id, but not overwrite existing)
// or neither (will use autogenerated id)
func (p Database) Insert(d interface{}) (string, string, error) {
	jsonBuf, id, rev, err := stripIdRev(d)
	if err != nil {
		return "", "", err
	}
	if id != "" && rev != "" {
		editRev, editErr := p.Edit(d)
		return id, editRev, editErr
	} else if id != "" {
		return p.insert(jsonBuf, id)
	} else if id == "" {
		return p.insert(jsonBuf, "")
	}
	return "", "", fmt.Errorf("invalid document")
}

// InsertWith inserts the given document 'd', using the passed 'id' as the _id. 
// The document should not contain "_id" or "_rev" tagged fields. 
// Returns the id and rev of the inserted document.
// Fails if the id already exists.
func (p Database) InsertWith(d interface{}, id string) (string, string, error) {
	jsonBuf, err := json.Marshal(d)
	if err != nil {
		return "", "", err
	}
	return p.insert(jsonBuf, id)
}

// Retrieve unmarshals the document matching 'id' to the given interface.
// It returns the current revision of that document.
func (p Database) Retrieve(id string, d interface{}) (string, error) {
	if id == "" {
		return "", fmt.Errorf("no id specified")
	}
	jsonBody, err := getURL(fmt.Sprintf("%s/%s", p.DBURL(), id))
	if err != nil {
		return "", fmt.Errorf("couldn't Retrieve %s: %s", id, err)
	}
	defer jsonBody.Close()
	jsonBytes, err := ioutil.ReadAll(jsonBody)
	if err != nil {
		return "", fmt.Errorf("couldn't read response for %s: %s", id, err)
	}
	jsonReader := bytes.NewReader(jsonBytes)
	idRev := &IdAndRev{}
	err = decodeJSON(jsonReader, idRev)
	if err != nil {
		return "", fmt.Errorf("couldn't decode id/rev for %s: %s", id, err)
	}
	jsonReader.Seek(0, 0)
	err = decodeJSON(jsonReader, d)
	if err != nil {
		return "", fmt.Errorf("couldn't decode document for %s: %s", id, err)
	}
	return idRev.Rev, nil
}

// RetrieveFast is the same as Retrieve, except it doesn't unmarshal the
// entire response into memory before returning, and (therefore) cannot
// return the current revision of the document.
func (p Database) RetrieveFast(id string, d interface{}) error {
	if id == "" {
		return fmt.Errorf("no id specified")
	}
	return unmarshalURL(fmt.Sprintf("%s/%s", p.DBURL(), id), d)
}

// Edit edits the given document, returning the new revision.
// The document must contain "_id" and "_rev" tagged fields.
func (p Database) Edit(d interface{}) (string, error) {
	jsonBuf, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	idRev := &IdAndRev{}
	err = json.Unmarshal(jsonBuf, idRev)
	if err != nil {
		return "", err
	}
	if idRev.Id == "" {
		return "", fmt.Errorf("id not specified")
	}
	if idRev.Rev == "" {
		return "", fmt.Errorf("rev not specified (try InsertWith)")
	}
	u := fmt.Sprintf("%s/%s", p.DBURL(), url.QueryEscape(idRev.Id))
	r := couchResponse{}
	if _, err = p.interact("PUT", u, defaultHeaders, jsonBuf, &r); err != nil {
		return "", err
	}
	return r.Rev, nil
}

// EditWith edits the given document, returning the new revision.
// The document should not contain "_id" or "_rev" tagged fields.
// If it does, they will be overwritten with the passed values.
func (p Database) EditWith(d interface{}, id, rev string) (string, error) {
	if id == "" || rev == "" {
		return "", fmt.Errorf("must specify both id and rev")
	}
	jsonBuf, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	m := map[string]interface{}{}
	err = json.Unmarshal(jsonBuf, &m)
	if err != nil {
		return "", err
	}
	m["_id"] = id
	m["_rev"] = rev
	return p.Edit(m)
}

// Delete deletes the document given by id and rev.
func (p Database) Delete(id, rev string) error {
	headers := map[string][]string{
		"If-Match": []string{rev},
	}
	u := fmt.Sprintf("%s/%s", p.DBURL(), id)
	r := couchResponse{}
	if _, err := p.interact("DELETE", u, headers, nil, &r); err != nil {
		return err
	}
	if !r.Ok {
		return fmt.Errorf(fmt.Sprintf("%s: %s", r.Error, r.Reason))
	}
	return nil
}

// insert makes a POST or PUT to insert the given document as represented
// by the jsonBuf buffer. If 'id' is non-empty, it's used in a PUT; otherwise,
// a POST is made, and an id is auto-generated.
// insert returns the id and rev of the inserted document.
func (p Database) insert(jsonBuf []byte, id string) (string, string, error) {
	r := couchResponse{}
	method, u := "POST", p.DBURL()
	if id != "" {
		method, u = "PUT", fmt.Sprintf("%s/%s", p.DBURL(), url.QueryEscape(id))
	}
	if _, err := p.interact(method, u, defaultHeaders, jsonBuf, &r); err != nil {
		return "", "", err
	}
	if !r.Ok {
		return "", "", fmt.Errorf(fmt.Sprintf("%s: %s", r.Error, r.Reason))
	}
	return r.Id, r.Rev, nil
}

// createDatabase makes the PUT which creates a new database.
func (p Database) createDatabase() error {
	r := couchResponse{}
	if _, err := p.interact("PUT", p.DBURL(), defaultHeaders, nil, &r); err != nil {
		return err
	}
	if !r.Ok {
		return fmt.Errorf("Create database operation returned not-OK")
	}
	return nil
}

// stripIdRev strips _id and _rev from the structure d, and returns the JSON-
// encoded form of that structure, along with id and rev separately, if they
// existed and were stripped.
func stripIdRev(d interface{}) (jsonBuf []byte, id, rev string, err error) {
	jsonBuf, err = json.Marshal(d)
	if err != nil {
		return
	}
	m := map[string]interface{}{}
	err = json.Unmarshal(jsonBuf, &m)
	if err != nil {
		return
	}
	idRev := &IdAndRev{}
	err = json.Unmarshal(jsonBuf, &idRev)
	if err != nil {
		return
	}
	if _, ok := m["_id"]; ok {
		id = idRev.Id
		delete(m, "_id")
	}
	if _, ok := m["_rev"]; ok {
		rev = idRev.Rev
		delete(m, "_rev")
	}
	jsonBuf, err = json.Marshal(m)
	return
}

type couchResponse struct {
	Ok     bool
	Id     string
	Rev    string
	Error  string
	Reason string
}

type KeyedViewResponse struct {
	TotalRows uint64 `json:"total_rows"`
	Offset    uint64 `json:"offset"`
	Rows      []Row  `json:"rows"`
}

type Row struct {
	Id  *string `json:"id"`
	Key *string `json:"key"`
}

type databaseInfo struct {
	Name string `json:"db_name"`
	// other stuff too, ignore for now
}


// Return array of document ids as returned by the given view/options combo.
// view should be eg. "_design/my_foo/_view/my_bar"
// options should be eg. { "limit": 10, "key": "baz" }
func (p Database) QueryIds(view string, options map[string]interface{}) ([]string, error) {
	kvr := &KeyedViewResponse{}
	if err := p.Query(view, options, kvr); err != nil {
		return make([]string, 0), err
	}
	ids := make([]string, len(kvr.Rows))
	i := 0
	for _, row := range kvr.Rows {
		if row.Id != nil {
			ids[i] = *row.Id
			i++
		}
	}
	return ids[:i], nil
}

func (p Database) Query(view string, options map[string]interface{}, results interface{}) error {
	if view == "" {
		return fmt.Errorf("empty view")
	}
	parameters := ""
	for k, v := range options {
		switch t := v.(type) {
		case string:
			parameters += fmt.Sprintf(`%s="%s"&`, k, url.QueryEscape(t))
		case int:
			parameters += fmt.Sprintf(`%s=%d&`, k, t)
		case bool:
			parameters += fmt.Sprintf(`%s=%v&`, k, t)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				panic(fmt.Sprintf("unsupported value-type %T in Query (%v)", t, err))
			}
			parameters += fmt.Sprintf(`%s=%v&`, k, string(b))
		}
	}
	fullUrl := fmt.Sprintf("%s/%s?%s", p.DBURL(), view, parameters)
	return unmarshalURL(fullUrl, results)
}
