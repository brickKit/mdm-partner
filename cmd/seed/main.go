// Command seed fills a running mdm/partner with the seed data for people (be-assembly-standard's seed and test data
// conventions): 80 partners (organizations and persons, Chinese and English names, enough to page through) with
// contacts and addresses; customer roles in the legal entities CN01 and ES01 of mdm/org's seed; and the negative data
// each eligibility answer needs: suspended and retired partners, a suspended and a retired role, a partner without a
// role in ES01, two partners sharing a tax ID (a merge candidate, D22). Partners are found again by their name before
// they are created, so a second run creates nothing; the legal entities are looked up by code in mdm/org (its seed
// runs first: make seed).
//
//	SEED_IAM_URL (http://localhost:18200)  the identity member's development exchange
//	SEED_USER (test.bob), SEED_PASSWORD     a test user granted mdm/partner's and mdm/org's operations
//	MDM_PARTNER_URL (http://localhost:18225), MDM_ORG_URL (http://localhost:18083)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

const prefix = "「本地测试」"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct{ base, token string }

func (c client) do(method, path string, body any, header ...string) (int, map[string]any) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (c client) must(method, path string, body any) map[string]any {
	code, out := c.do(method, path, body)
	if code != 200 {
		fail("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed mdm/partner: "+format+"\n", a...)
	os.Exit(1)
}

func token() string {
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test users' password in Casdoor)")
	}
	b, _ := json.Marshal(map[string]string{"username": env("SEED_USER", "test.bob"), "password": pw})
	resp, err := http.Post(env("SEED_IAM_URL", "http://localhost:18200")+"/api/iam/dev/token", "application/json", bytes.NewReader(b))
	if err != nil {
		fail("the identity member is not reachable: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		fail("the development exchange refused the seed user: %d", resp.StatusCode)
	}
	return out.AccessToken
}

// legalEntity is the ID of mdm/org's seeded legal entity with the code.
func legalEntity(org client, code string) string {
	_, out := org.do("GET", "/api/org/legal-entities?limit=200&q="+url.QueryEscape(code), nil)
	for _, e := range out["legal_entities"].([]any) {
		if e.(map[string]any)["code"] == code {
			return e.(map[string]any)["id"].(string)
		}
	}
	fail("mdm/org has no legal entity %s: run its seed first", code)
	return ""
}

func main() {
	base := env("MDM_PARTNER_URL", "http://localhost:18225")
	if r, err := http.Get(base + "/readyz"); err != nil || r.StatusCode != 200 { // probe before pouring
		fail("mdm/partner at %s is not ready", base)
	}
	t := token()
	c, org := client{base, t}, client{env("MDM_ORG_URL", "http://localhost:18083"), t}
	cn, es := legalEntity(org, "CN01"), legalEntity(org, "ES01")
	cities := []string{"上海", "北京", "广州", "深圳", "杭州", "成都", "武汉", "南京"}
	trades := []string{"贸易", "科技", "食品", "物流", "茶业", "咖啡", "设备", "零售"}
	partner := func(n int, name string, body map[string]any) string {
		full := prefix + name
		_, found := c.do("GET", "/api/partner/partners?limit=200&q="+url.QueryEscape(full), nil)
		for _, p := range found["partners"].([]any) {
			if p.(map[string]any)["name"] == full {
				return p.(map[string]any)["id"].(string)
			}
		}
		body["name"] = full
		code, out := c.do("POST", "/api/partner/partners", body, "Idempotency-Key", fmt.Sprintf("seed-partner-%03d", n))
		if code != 200 {
			fail("partner %s: %d %v", name, code, out)
		}
		return out["partner"].(map[string]any)["id"].(string)
	}
	var made []string
	for i := 0; i < 64; i++ {
		city, trade := cities[i%len(cities)], trades[(i/len(cities))%len(trades)]
		name := fmt.Sprintf("%s%s%s有限公司 %02d", city, "演示", trade, i)
		made = append(made, partner(i, name, map[string]any{"kind": "organization", "country": "CN",
			"tax_id":    fmt.Sprintf("91310000SEED%04dX", i),
			"contacts":  []map[string]any{{"name": "联系人" + fmt.Sprint(i), "role": "purchasing", "email": fmt.Sprintf("buyer%02d@example.com", i)}},
			"addresses": []map[string]any{{"kind": "billing", "lines": []string{fmt.Sprintf("演示路 %d 号", 100+i)}, "city": city, "country": "CN"}}}))
	}
	for i, n := range []string{"Iberia Demo Foods S.L.", "Madrid Coffee Trading S.A.", "Barcelona Tea House S.L.",
		"Valencia Equipment S.L.", "Sevilla Logistics S.A.", "Bilbao Retail S.L."} {
		made = append(made, partner(100+i, n, map[string]any{"kind": "organization", "country": "ES",
			"tax_id":    fmt.Sprintf("B%08d", 1000+i),
			"addresses": []map[string]any{{"kind": "registered", "lines": []string{"Calle Demo " + fmt.Sprint(10+i)}, "city": "Madrid", "country": "ES"}}}))
	}
	for i, n := range []string{"张伟", "李娜", "王芳", "Lucía García", "Carlos Martín"} {
		country := "CN"
		if i >= 3 {
			country = "ES"
		}
		made = append(made, partner(200+i, n, map[string]any{"kind": "person", "country": country}))
	}
	// a merge candidate: the same tax ID in CN as partner 0 (pointed out, merged by hand, D22)
	partner(300, "上海演示贸易有限公司（重复录入）", map[string]any{"kind": "organization", "country": "CN", "tax_id": "91310000SEED0000X"})
	suspended := partner(301, "暂停合作的客户", map[string]any{"kind": "organization", "country": "CN"})
	retired := partner(302, "已退役的客户", map[string]any{"kind": "organization", "country": "CN"})

	roles := 0
	role := func(p, le string, body map[string]any) {
		code, out := c.do("PUT", "/api/partner/partners/"+p+"/roles/customer/"+le, body)
		if code != 200 && !(code == 409 && out["reason"] == "STATE_CONFLICT") { // a retired role stays retired
			fail("role %s/%s: %d %v", p, le, code, out)
		}
		roles++
	}
	for i, p := range made {
		role(p, cn, map[string]any{"default_currency": "CNY", "payment_terms": []string{"NET30", "NET60", "COD"}[i%3]})
		if i%2 == 0 { // every other partner also sells in ES01; the rest have no role there (role_not_opened)
			role(p, es, map[string]any{"default_currency": "EUR", "payment_terms": "NET30"})
		}
	}
	for _, p := range []string{suspended, retired} {
		role(p, cn, map[string]any{"default_currency": "CNY"})
	}
	state := func(path, to string) {
		if code, out := c.do("POST", path, map[string]any{"state": to}); code != 200 && !(code == 409 && out["reason"] == "STATE_CONFLICT") {
			fail("%s -> %s: %d %v", path, to, code, out)
		}
	}
	state("/api/partner/partners/"+suspended+"/state", "suspended")
	state("/api/partner/partners/"+retired+"/state", "retired")
	state("/api/partner/partners/"+made[1]+"/roles/customer/"+cn+"/state", "suspended")
	state("/api/partner/partners/"+made[3]+"/roles/customer/"+cn+"/state", "retired")
	fmt.Printf("seed mdm/partner: %d partners, %d roles, 2 negative partners, 2 negative roles, 1 merge candidate\n",
		len(made)+3, roles)
}
