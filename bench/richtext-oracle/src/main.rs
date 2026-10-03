//! Differential test against the Rails pipeline: tests/corpus/expected.json is produced by
//! reference-tools/richtext/run.sh in the campfire-reference image. Every output is also checked
//! against security properties that don't depend on the oracle.





use campfire_richtext::{
    AttachableResolver, GidLookup, MentionUser, Presentation, RenderContext, SignedLookup, editable_value, mentioned_users,
    present_message, to_plain_text,
};
use serde_json::Value;

struct Corpus {
    json: Value,
}

impl Corpus {
    fn load() -> Corpus {
        // RICHTEXT_CORPUS points at a larger, uncommitted corpus (see reference-tools/richtext/run.sh)
        let path = std::env::var("RICHTEXT_CORPUS")
            .unwrap_or_else(|_| concat!(env!("CARGO_MANIFEST_DIR"), "/tests/corpus/expected.json").to_string());
        let text = std::fs::read_to_string(&path).expect("run reference-tools/richtext/run.sh to generate the corpus");
        Corpus { json: serde_json::from_str(&text).unwrap() }
    }

    fn users(&self) -> Vec<MentionUser> {
        self.json["users"]
            .as_array()
            .unwrap()
            .iter()
            .map(|u| MentionUser {
                id: u["id"].as_i64().unwrap(),
                name: u["name"].as_str().unwrap().into(),
                title: u["title"].as_str().unwrap().into(),
                attachable_sgid: u["attachable_sgid"].as_str().unwrap().into(),
                user_path: u["user_path"].as_str().unwrap().into(),
                avatar_path: u["avatar_path"].as_str().unwrap().into(),
            })
            .collect()
    }
}

/// Stands in for the app: SGIDs Rails minted are "verified" by exact match, and GIDs are looked
/// up by model and id.
struct TestResolver {
    users: Vec<MentionUser>,
    rooms: Vec<i64>,
    signed: Vec<(String, String, i64, bool)>,
}

impl TestResolver {
    fn from(corpus: &Corpus) -> Self {
        TestResolver {
            users: corpus.users(),
            rooms: corpus.json["rooms"].as_array().unwrap().iter().map(|r| r.as_i64().unwrap()).collect(),
            signed: corpus.json["signed"]
                .as_array()
                .unwrap()
                .iter()
                .map(|s| {
                    (
                        s["sgid"].as_str().unwrap().into(),
                        s["model"].as_str().unwrap().into(),
                        s["id"].as_i64().unwrap(),
                        s["exists"].as_bool().unwrap(),
                    )
                })
                .collect(),
        }
    }
}

impl AttachableResolver for TestResolver {
    fn locate_signed(&self, sgid: &str) -> SignedLookup {
        match self.signed.iter().find(|(s, ..)| s == sgid) {
            Some((_, model, id, true)) if model == "User" => SignedLookup::User(self.users.iter().find(|u| u.id == *id).unwrap().clone()),
            Some((_, model, _, _)) => SignedLookup::MissingRecord { model_name: model.clone() },
            None => SignedLookup::Invalid,
        }
    }

    fn find_gid(&self, gid: &str) -> GidLookup {
        // GlobalID's default locator ignores the app name
        let Some(rest) = gid.strip_prefix("gid://") else { return GidLookup::NotFound };
        if !gid.is_ascii() {
            return GidLookup::NotFound;
        }
        let Some((_app, rest)) = rest.split_once('/') else { return GidLookup::NotFound };
        let rest = rest.split('?').next().unwrap();
        let Some((model, id)) = rest.split_once('/') else { return GidLookup::NotFound };
        let Ok(id) = id.parse::<i64>() else { return GidLookup::NotFound };
        match model {
            "User" => self.users.iter().find(|u| u.id == id).cloned().map_or(GidLookup::NotFound, GidLookup::User),
            "Room" if self.rooms.contains(&id) => GidLookup::OtherModel,
            _ => GidLookup::NotFound,
        }
    }
}


fn outcome<T: serde::Serialize>(value: Result<T, campfire_richtext::Error>) -> Value {
 match value {Ok(v)=>serde_json::json!({"ok":v}),Err(e)=>serde_json::json!({"error":e.to_string()})}
}
fn main() {
 let corpus=Corpus::load();let resolver=TestResolver::from(&corpus);let mut cases=Vec::new();
 for case in corpus.json["cases"].as_array().unwrap() {
  let ctx=RenderContext {resolver:&resolver,request_host:case["host"].as_str().map(str::to_string)};let body=case["body"].as_str().unwrap();
  let presentation=match present_message(body,&ctx){Presentation::Html(html)=>serde_json::json!({"ok":html}),Presentation::Unrenderable=>serde_json::json!({"error":"unrenderable"})};
  let filtered=campfire_richtext::Content::load(body,&ctx).and_then(|c|campfire_richtext::filters::apply(c,&ctx)).map(|c|c.to_html());
  let rendered=campfire_richtext::Content::load(body,&ctx).and_then(|c|c.to_rendered_html_with_layout(&ctx));
  cases.push(serde_json::json!({"name":case["name"],"body":body,"host":case["host"],"presentation":presentation,"plain_text":outcome(to_plain_text(body,&ctx)),"editable":outcome(editable_value(body,&ctx)),"filtered":outcome(filtered),"body_html":outcome(rendered),"mentioned":outcome(mentioned_users(body,&ctx).map(|users|users.into_iter().map(|u|u.id).collect::<Vec<_>>()))}));
 }
 println!("{}",serde_json::json!({"users":corpus.json["users"],"signed":corpus.json["signed"],"cases":cases}));
}
