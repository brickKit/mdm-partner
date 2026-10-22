// Command seed fills a running mdm/partner with the seed data for people (be-assembly-standard's seed and test data
// conventions):
//
//   - access, by the authorization admin: the roles maintain (the seed user, test.mdm.admin), read (test.mdm.viewer and
//     test.unit.admin.cn) and customer roles (test.sales.es); the management unit 「中国区客户」 limited to granting the reading
//     of partners and the keeping of customer roles (operations, not countries: partners have no dimension yet), held by test.unit.admin.cn, who grants test.sales.cn and test.sales.multi
//     within it; its attempt to grant editing partners is refused (the refusal is kept at the effect point);
//   - 209 partners: organizations in China (120), Spain (30), the United States (25) and Hong Kong (6), 25 persons and
//     3 negative ones, enough to page through and to search in Chinese, pinyin and English; each organization has a
//     billing address, one to three shipping addresses (some a registered one too) and one or two contacts; each person
//     a shipping address and a contact;
//   - customer roles in every usable legal entity, in local and foreign currencies with varied payment terms; roles in
//     HK01, which is suspended: they were opened while it was active (the seed resumes HK01, opens them and suspends it
//     again, only while they are missing), so a document for them meets a legal entity that cannot be used;
//   - the negative data each eligibility answer needs: a suspended and a retired partner, suspended and retired roles,
//     partners without a role in ES01 or US01, two partners sharing a tax ID (a merge candidate, D22).
//
// Idempotency keys: the first seed's 0-63, 100-105, 200-204 and 300-302 stay; the partners added later take
// 1000-1119 (China), 1500-1529 (Spain), 2000-2024 (the US), 2100-2105 (Hong Kong) and 2200-2224 (persons).
// Partners are found again by their name before they are created and their contacts and addresses are set only when
// they differ, so a second run changes nothing; the legal entities are looked up by code in mdm/org (its seed runs
// first: make seed).
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the user that writes the data; the test accounts' password
//	MDM_PARTNER_URL (http://localhost:18225), MDM_ORG_URL (http://localhost:18083)
package main

import (
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const prefix = "「本地测试」"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed mdm/partner: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

type client struct{ seed.Client }

func (c client) do(method, path string, body any, header ...string) (int, map[string]any) {
	code, out, err := c.Do(method, path, body, header...)
	check(err)
	return code, out
}

func (c client) must(method, path string, body any) map[string]any {
	out, err := c.Must(method, path, body)
	check(err)
	return out
}

// access sets up who may do what with partners and answers the seed user's token once its grant has reached mdm/partner.
func access(base string) string {
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	effect := env("SEED_AUTHZ_URL", "http://localhost:18223")
	accounts, err := seed.Accounts(iam, pw, admin, user, "test.mdm.admin", "test.mdm.viewer", "test.unit.admin.cn",
		"test.sales.cn", "test.sales.es", "test.sales.multi")
	check(err)
	a, err := seed.NewAuthz(effect, accounts[admin].Token, "mdm/partner")
	check(err)
	read := []sdkauthz.Segment{seed.Segment("mdm.partner", "mdm.partner.read"), seed.Segment("mdm.partner_role", "mdm.partner_role.read")}
	customerRoles := append(append([]sdkauthz.Segment{}, read...), seed.Segment("mdm.partner_role", "mdm.partner_role.manage"))
	maintain := append(append([]sdkauthz.Segment{}, customerRoles...), seed.Segment("mdm.partner", "mdm.partner.create"),
		seed.Segment("mdm.partner", "mdm.partner.edit"), seed.Segment("mdm.partner", "mdm.partner.merge"))
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "maintain", Name: prefix + "伙伴维护", To: []string{user, "test.mdm.admin"}, Segments: maintain},
		{Key: "read", Name: prefix + "伙伴只读", To: []string{"test.mdm.viewer", "test.unit.admin.cn"}, Segments: read},
		{Key: "customer-roles", Name: prefix + "客户角色维护", To: []string{"test.sales.es"}, Segments: customerRoles},
	}, accounts)
	check(err)
	// delegated administration: the China unit's admin grants within the unit, and nothing beyond it
	ops := []string{"mdm.partner.read", "mdm.partner_role.read", "mdm.partner_role.manage"}
	unit, err := a.Unit("cn-customers", "", prefix+"中国区客户", customerRoles, ops)
	check(err)
	ua := accounts["test.unit.admin.cn"]
	_, err = a.Hold("cn-customers", unit, ua.Principal)
	check(err)
	if err := seed.NewClient(effect, ua.Token).WaitGranted("POST", "/api/authz/changes", 30*time.Second); err != nil {
		fail("test.unit.admin.cn cannot use the effect point (run infra/authz's seed first): %v", err)
	}
	unitAdmin := a.As(ua.Token)
	for _, u := range []string{"test.sales.cn", "test.sales.multi"} {
		_, err := unitAdmin.Grant("cn-sales", unit, accounts[u].Principal, customerRoles)
		check(err)
	}
	_, err = unitAdmin.Grant("cn-beyond", unit, accounts["test.sales.cn"].Principal,
		[]sdkauthz.Segment{seed.Segment("mdm.partner", "mdm.partner.edit")})
	if err == nil || !strings.Contains(err.Error(), "CONTAINMENT_NOT_PROVEN") {
		fail("the unit admin's grant beyond the unit was not refused: %v", err)
	}
	check(seed.NewClient(base, accounts[user].Token).WaitGranted("POST", "/api/partner/partners", 30*time.Second))
	return accounts[user].Token
}

// legalEntity is the ID of mdm/org's seeded legal entity with the code.
func legalEntity(org client, code string) string {
	_, out := org.do("GET", "/api/org/legal-entities?limit=200&q="+url.QueryEscape(code), nil)
	for _, e := range out["legal_entities"].([]any) {
		if e := e.(map[string]any); e["code"] == code {
			return e["id"].(string)
		}
	}
	fail("mdm/org has no legal entity %s: run its seed first", code)
	return ""
}

// sameItems compares contacts or addresses without their IDs (the component gives each an ID).
func sameItems(got any, want []map[string]any) bool {
	items, _ := got.([]any)
	if len(items) != len(want) {
		return false
	}
	for i, it := range items {
		m, _ := it.(map[string]any)
		for k, v := range want[i] {
			if lines, ok := v.([]string); ok {
				var s []string
				gl, _ := m[k].([]any)
				for _, l := range gl {
					s = append(s, fmt.Sprint(l))
				}
				if !reflect.DeepEqual(s, lines) {
					return false
				}
			} else if fmt.Sprint(m[k]) != fmt.Sprint(v) {
				return false
			}
		}
	}
	return true
}

var surnames = []string{"王", "李", "张", "刘", "陈", "杨", "黄", "赵", "周", "吴", "徐", "孙"}
var given = []string{"敏", "伟", "芳", "强", "静", "磊", "洋", "艳", "军", "婷"}
var westNames = []string{"Ana Pérez", "Luis Gómez", "Marta Ruiz", "David Torres", "Laura Díaz", "Sarah Miller",
	"James Wilson", "Olivia Davis", "Daniel Moore", "Chloe Taylor"}

func contacts(i int, west bool, n int) []map[string]any {
	var out []map[string]any
	for k, role := range []string{"purchasing", "finance"}[:n] {
		name := surnames[(i+k)%len(surnames)] + given[(i*3+k)%len(given)]
		if west {
			name = westNames[(i+k)%len(westNames)]
		}
		out = append(out, map[string]any{"name": name, "role": role, "email": fmt.Sprintf("%s%03d@example.com", role, i),
			"phone": fmt.Sprintf("+00 555 %04d", 1000+i*2+k)})
	}
	return out
}

func addresses(i int, country, city, street string) []map[string]any {
	out := []map[string]any{{"kind": "billing", "lines": []string{fmt.Sprintf("%s %d", street, 100+i)}, "city": city, "country": country}}
	for k := 0; k < 1+i%3; k++ { // one to three places to deliver to
		out = append(out, map[string]any{"kind": "shipping", "lines": []string{fmt.Sprintf("%s %d", street, 300+10*i+k),
			fmt.Sprintf("仓库 %d / Warehouse %d", k+1, k+1)}, "city": city, "country": country})
	}
	if i%4 == 0 {
		out = append(out, map[string]any{"kind": "registered", "lines": []string{fmt.Sprintf("%s %d", street, 1+i)}, "city": city, "country": country})
	}
	return out
}

type made struct {
	id, country string
	i           int
}

func main() {
	base := env("MDM_PARTNER_URL", "http://localhost:18225")
	if !seed.Ready(base) { // probe before pouring
		fail("mdm/partner at %s is not ready", base)
	}
	t := access(base)
	c, org := client{seed.NewClient(base, t)}, client{seed.NewClient(env("MDM_ORG_URL", "http://localhost:18083"), t)}
	les := map[string]string{}
	for _, code := range []string{"CN01", "ES01", "US01", "HK01"} {
		les[code] = legalEntity(org, code)
	}

	// partner finds or creates a partner by name, then sets its contacts and addresses when they differ
	partner := func(n int, name string, body map[string]any) string {
		full := prefix + name
		id := ""
		_, found := c.do("GET", "/api/partner/partners?limit=200&q="+url.QueryEscape(full), nil)
		for _, p := range found["partners"].([]any) {
			if p.(map[string]any)["name"] == full {
				id = p.(map[string]any)["id"].(string)
			}
		}
		if id == "" {
			body["name"] = full
			code, out := c.do("POST", "/api/partner/partners", body, "Idempotency-Key", fmt.Sprintf("seed-partner-%03d", n))
			if code != 200 {
				fail("partner %s: %d %v", name, code, out)
			}
			return out["partner"].(map[string]any)["id"].(string)
		}
		cur := c.must("GET", "/api/partner/partners/"+id, nil)
		if p, ok := cur["partner"].(map[string]any); ok {
			cur = p
		}
		patch := map[string]any{}
		for _, k := range []string{"contacts", "addresses"} {
			if want, ok := body[k].([]map[string]any); ok && !sameItems(cur[k], want) {
				patch[k] = want
			}
		}
		if len(patch) > 0 {
			c.must("PATCH", "/api/partner/partners/"+id, patch)
		}
		return id
	}

	var orgs, persons []made
	// China: the first 64 keep the names and keys of the first seed (fixed identifiers), 56 more from other cities
	cities := []string{"上海", "北京", "广州", "深圳", "杭州", "成都", "武汉", "南京"}
	cities2 := []string{"苏州", "天津", "重庆", "西安", "厦门", "青岛", "长沙", "郑州"}
	trades := []string{"贸易", "科技", "食品", "物流", "茶业", "咖啡", "设备", "零售"}
	for i := 0; i < 120; i++ {
		city, trade, key := cities[i%8], trades[(i/8)%8], i
		if i >= 64 {
			city, trade, key = cities2[(i-64)%8], trades[((i-64)/8)%8], 1000+i
		}
		id := partner(key, fmt.Sprintf("%s演示%s有限公司 %02d", city, trade, i), map[string]any{"kind": "organization",
			"country": "CN", "tax_id": fmt.Sprintf("91310000SEED%04dX", i), "contacts": contacts(i, false, 1+i%2),
			"addresses": addresses(i, "CN", city, "演示路")})
		orgs = append(orgs, made{id, "CN", i})
	}
	esNames := []string{"Iberia Demo Foods S.L.", "Madrid Coffee Trading S.A.", "Barcelona Tea House S.L.",
		"Valencia Equipment S.L.", "Sevilla Logistics S.A.", "Bilbao Retail S.L."}
	for _, city := range []string{"Toledo", "Granada", "Málaga", "Zaragoza", "Murcia", "Alicante"} {
		for _, kind := range []string{"Demo Foods S.L.", "Demo Coffee S.A.", "Demo Tea Trading S.L.", "Demo Equipment S.L."} {
			esNames = append(esNames, city+" "+kind)
		}
	}
	for i, n := range esNames {
		key := 100 + i
		if i >= 6 {
			key = 1500 + i
		}
		id := partner(key, n, map[string]any{"kind": "organization", "country": "ES", "tax_id": fmt.Sprintf("B%08d", 1000+i),
			"contacts": contacts(i, true, 1+i%2), "addresses": addresses(i, "ES", "Madrid", "Calle Demo")})
		orgs = append(orgs, made{id, "ES", i})
	}
	firstUS := len(orgs)
	n := 0
	for _, city := range []string{"Boston", "Chicago", "Seattle", "Austin", "Denver"} {
		for _, kind := range []string{"Demo Coffee Co.", "Demo Tea Imports LLC", "Demo Restaurant Supply Inc.",
			"Demo Retail Group Inc.", "Demo Logistics LLC"} {
			id := partner(2000+n, city+" "+kind, map[string]any{"kind": "organization", "country": "US",
				"tax_id": fmt.Sprintf("%02d-%07d", 10+n, 1234500+n), "contacts": contacts(n, true, 1+n%2),
				"addresses": addresses(n, "US", city, "Demo Street")})
			orgs = append(orgs, made{id, "US", n})
			n++
		}
	}
	for i, name := range []string{"香港演示茶行有限公司", "九龍演示咖啡貿易有限公司", "新界演示設備有限公司", "港島演示零售有限公司",
		"荃灣演示物流有限公司", "沙田演示食品有限公司"} {
		id := partner(2100+i, name, map[string]any{"kind": "organization", "country": "HK", "tax_id": fmt.Sprintf("%08d", 51000000+i),
			"contacts": contacts(i, false, 1), "addresses": addresses(i, "HK", "香港", "演示道")})
		orgs = append(orgs, made{id, "HK", i})
	}
	type person struct{ name, country string }
	people := []person{{"张伟", "CN"}, {"李娜", "CN"}, {"王芳", "CN"}, {"Lucía García", "ES"}, {"Carlos Martín", "ES"}}
	for _, name := range []string{"赵敏", "钱伟", "孙丽", "周杰", "吴芳", "郑浩", "冯雪", "陈晨", "褚明", "卫兰"} {
		people = append(people, person{name, "CN"})
	}
	people = append(people, person{"María López", "ES"}, person{"Javier Sánchez", "ES"}, person{"John Smith", "US"},
		person{"Emily Johnson", "US"}, person{"Michael Brown", "US"}, person{"Sophia Lee", "US"}, person{"Ethan Clark", "US"},
		person{"Isabella Lewis", "US"}, person{"Noah Walker", "US"}, person{"Mia Hall", "US"})
	for i, p := range people {
		key := 200 + i
		if i >= 5 {
			key = 2200 + i
		}
		city := map[string]string{"CN": "上海", "ES": "Madrid", "US": "Boston"}[p.country]
		id := partner(key, p.name, map[string]any{"kind": "person", "country": p.country,
			"contacts": []map[string]any{{"name": p.name, "role": "self", "email": fmt.Sprintf("person%03d@example.com", i)}},
			"addresses": []map[string]any{{"kind": "shipping", "lines": []string{fmt.Sprintf("演示小区 %d 号 / Demo Home %d", i+1, i+1)},
				"city": city, "country": p.country}}})
		persons = append(persons, made{id, p.country, i})
	}
	// a merge candidate: the same tax ID in CN as partner 0 (pointed out, merged by hand, D22)
	partner(300, "上海演示贸易有限公司（重复录入）", map[string]any{"kind": "organization", "country": "CN", "tax_id": "91310000SEED0000X"})
	suspended := partner(301, "暂停合作的客户", map[string]any{"kind": "organization", "country": "CN"})
	retired := partner(302, "已退役的客户", map[string]any{"kind": "organization", "country": "CN"})

	roles := 0
	role := func(p, le, ccy, terms string) {
		code, out := c.do("PUT", "/api/partner/partners/"+p+"/roles/customer/"+les[le], map[string]any{"default_currency": ccy, "payment_terms": terms})
		if code != 200 && !(code == 409 && out["reason"] == "STATE_CONFLICT") { // a retired role stays retired
			fail("role %s/%s: %d %v", p, le, code, out)
		}
		roles++
	}
	terms := []string{"NET30", "NET60", "COD", "NET15", "PREPAID", "NET45"}
	var hk []string // partners with a role in HK01, opened below while it is active
	for _, o := range orgs {
		t := terms[o.i%len(terms)]
		switch o.country {
		case "CN":
			role(o.id, "CN01", "CNY", t)
			if o.i%2 == 0 { // every other one also sells in ES01; the rest have no role there (role_not_opened)
				role(o.id, "ES01", "EUR", "NET30")
			}
			if o.i%5 == 0 {
				role(o.id, "US01", "USD", "NET45")
			}
		case "ES":
			role(o.id, "ES01", "EUR", t)
			if o.i%3 == 0 { // a customer of CN01 billed in euros
				role(o.id, "CN01", "EUR", "NET60")
			}
		case "US":
			role(o.id, "US01", "USD", t)
			if o.i%4 == 0 { // a customer of CN01 billed in dollars
				role(o.id, "CN01", "USD", "NET30")
			}
		case "HK":
			hk = append(hk, o.id)
			if o.i < 2 {
				role(o.id, "CN01", "HKD", "NET30")
			}
		}
	}
	for _, p := range persons {
		role(p.id, map[string]string{"CN": "CN01", "ES": "ES01", "US": "US01"}[p.country],
			map[string]string{"CN": "CNY", "ES": "EUR", "US": "USD"}[p.country], "COD")
	}
	for _, p := range []string{suspended, retired} {
		role(p, "CN01", "CNY", "NET30")
	}
	// HK01: open what is missing while it is active, then suspend it again (business came before the suspension)
	var missing []string
	for _, p := range hk {
		has := false
		for _, x := range c.must("GET", "/api/partner/partners/"+p+"/roles", nil)["roles"].([]any) {
			has = has || x.(map[string]any)["legal_entity_id"] == les["HK01"]
		}
		if !has {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		org.must("POST", "/api/org/legal-entities/"+les["HK01"]+"/state", map[string]any{"state": "active"})
		for _, p := range missing {
			role(p, "HK01", "HKD", "NET30")
		}
		org.must("POST", "/api/org/legal-entities/"+les["HK01"]+"/state", map[string]any{"state": "suspended"})
	}
	roles += len(hk) - len(missing)
	state := func(path, to string) {
		if code, out := c.do("POST", path, map[string]any{"state": to}); code != 200 && !(code == 409 && out["reason"] == "STATE_CONFLICT") {
			fail("%s -> %s: %d %v", path, to, code, out)
		}
	}
	state("/api/partner/partners/"+suspended+"/state", "suspended")
	state("/api/partner/partners/"+retired+"/state", "retired")
	state("/api/partner/partners/"+orgs[1].id+"/roles/customer/"+les["CN01"]+"/state", "suspended")
	state("/api/partner/partners/"+orgs[3].id+"/roles/customer/"+les["CN01"]+"/state", "retired")
	state("/api/partner/partners/"+orgs[firstUS].id+"/roles/customer/"+les["US01"]+"/state", "suspended")
	fmt.Printf("seed mdm/partner: %d partners (%d organizations, %d persons, 3 negative), %d customer roles in CN01, ES01, "+
		"US01 and the suspended HK01, 3 negative roles, 1 merge candidate; unit 中国区客户 with its admin\n",
		len(orgs)+len(persons)+3, len(orgs), len(persons), roles)
}
