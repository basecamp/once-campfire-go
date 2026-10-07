"""Seed-grounded contracts for full-document authenticated message forms."""
from html.parser import HTMLParser


class Elements(HTMLParser):
    def __init__(self, body):
        super().__init__(convert_charrefs=True)
        self.tags = []
        self.forms = []
        self.form = None
        self.ends = []
        self.contents = []
        self.content = None
        self.feed(body.decode())

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        self.tags.append((tag, attrs))
        if tag == 'form':
            self.form = (attrs, [])
            self.forms.append(self.form)
        elif self.form is not None:
            self.form[1].append((tag, attrs))
        if tag == 'span' and attrs.get('data-boost-delete-target') == 'content':
            self.content = []

    def handle_data(self, text):
        if self.content is not None:
            self.content.append(text)

    def handle_endtag(self, tag):
        self.ends.append(tag)
        if tag == 'form':
            self.form = None
        if tag == 'span' and self.content is not None:
            self.contents.append(''.join(self.content))
            self.content = None

    def require(self, tag, **attrs):
        found = [a for t, a in self.tags if t == tag and all(a.get(k) == v for k, v in attrs.items())]
        if len(found) != 1:
            raise RuntimeError(f'expected one {tag} with {attrs}, got {len(found)}')
        return found[0]


def form_control(elements, action, tag, **attrs):
    forms = [children for a, children in elements.forms if a.get('action') == action]
    if len(forms) != 1 or not any(t == tag and all(a.get(k) == v for k, v in attrs.items()) for t, a in forms[0]):
        raise RuntimeError(f'missing {tag} {attrs} in form {action}')


def seed_message(db, message_id):
    row = db.execute("SELECT m.id,m.room_id,m.client_message_id,coalesce(r.body,'') FROM messages m LEFT JOIN action_text_rich_texts r ON r.record_type='Message' AND r.record_id=m.id AND r.name='body' WHERE m.id=?", (message_id,)).fetchone()
    if row is None:
        raise RuntimeError(f'missing seed message {message_id}')
    return dict(zip(('id', 'room', 'client', 'body'), row))


def contract(route, body, message, boosts):
    e = Elements(body)
    if not body.lstrip().lower().startswith(b'<!doctype html>') or not body.rstrip().lower().endswith(b'</html>') or 'body' not in e.ends or 'head' not in e.ends:
        raise RuntimeError('missing complete full-document layout')
    for tag in ['html', 'head', 'body']:
        e.require(tag)
    e.require('footer', id='footer')
    mid, room, client = message['id'], message['room'], message['client']
    nested = f'/rooms/{room}/messages/{mid}'
    if route.startswith('message_edit'):
        e.require('turbo-frame', id=f'edit_message_{client}')
        e.require('form', id=f'delete_form_message_{client}', action=nested, method='post')
        e.require('input', name='_method', value='delete')
        # Edit PATCH and DELETE share an action, but have distinct form IDs.
        deletion = [children for a, children in e.forms if a.get('id') == f'delete_form_message_{client}']
        if len(deletion) != 1 or not any(t == 'input' and a.get('name') == '_method' and a.get('value') == 'delete' for t, a in deletion[0]):
            raise RuntimeError('missing edit deletion control')
        e.require('button', form=f'delete_form_message_{client}', type='submit')
        if route == 'message_edit':
            e.require('form', id=f'form_message_{client}', action=nested, method='post')
            e.require('input', name='_method', value='patch')
            editor = e.require('lexxy-editor', name='message[body]')
            if editor.get('value') != message['body']:
                raise RuntimeError('editor value differs from seed body')
            return [client, nested, editor['value']]
        if any(t == 'lexxy-editor' and a.get('name') == 'message[body]' for t, a in e.tags):
            raise RuntimeError('attachment edit unexpectedly includes body editor')
        e.require('a', **{'href': nested, 'class': 'message__action-btn message__edit-close-btn txt-small btn btn--borderless'})
        if b'launch-notes.txt' not in body or b'/rails/active_storage/' not in body:
            raise RuntimeError('missing complete attachment presentation')
        return [client, nested, 'launch-notes.txt']
    if route == 'new_boost':
        e.require('turbo-frame', id=f'new_boost_message_{client}')
        e.require('form', action=f'/messages/{mid}/boosts', method='post')
        field = e.require('input', name='boost[content]', maxlength='16')
        if 'required' not in field:
            raise RuntimeError('boost content is not required')
        e.require('a', **{'href': f'/messages/{mid}/boosts', 'data-turbo-frame': f'boosts_message_{client}'})
        return [client, str(mid)]
    e.require('turbo-frame', id=f'boosting_message_{client}')
    e.require('div', id=f'boosts_message_{client}')
    e.require('turbo-frame', id=f'new_boost_message_{client}')
    e.require('a', href=f'/messages/{mid}/boosts/new')
    observed = [(int(a['id'][6:]), int(a['data-boost-delete-booster-id-value'])) for t, a in e.tags if t == 'div' and a.get('id', '').startswith('boost_') and 'data-boost-delete-booster-id-value' in a]
    expected = [(bid, uid) for bid, uid, _ in boosts]
    if observed != expected or e.contents != [content for _, _, content in boosts]:
        raise RuntimeError(f'ordered boost contract mismatch: {observed}, {e.contents}')
    for bid, _, _ in boosts:
        action = f'/messages/{mid}/boosts/{bid}'
        e.require('form', action=action, method='post')
        form_control(e, action, 'input', name='_method', value='delete')
        form_control(e, action, 'button', type='submit')
    return [client, *[[str(bid), str(uid), content] for bid, uid, content in boosts]]
