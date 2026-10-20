"""Match fixed-rate POST receipts to their exact persisted message population."""


def verify(connection, after_id, room, creator, sample):
    try:
        acks = sample['acknowledged']
        prefix = sample['client_message_prefix']
        expected = sample['ok']
    except KeyError as error:
        raise RuntimeError('fixed POST client lacks receipt metadata; rebuild bench/httprate') from error
    if (type(expected) is not int or expected < 0 or not isinstance(prefix, str) or not prefix
            or not isinstance(acks, list) or any(
                not isinstance(pair, list) or len(pair) != 2
                or any(type(value) is not int for value in pair) or pair[0] < 0 or pair[1] <= 0
                for pair in acks)):
        raise RuntimeError('invalid fixed POST receipt metadata')
    rows = connection.execute("""
        SELECT m.id, m.room_id, m.creator_id, m.client_message_id,
               rt.body, idx.body
        FROM messages m
        JOIN action_text_rich_texts rt ON rt.record_type='Message'
             AND rt.record_id=m.id AND rt.name='body'
        JOIN message_search_index idx ON idx.rowid=m.id
        WHERE m.id>?
    """, (after_id,)).fetchall()
    count = connection.execute('SELECT count(*) FROM messages WHERE id>?', (after_id,)).fetchone()[0]
    if len(acks) != expected or count != expected or len(rows) != expected:
        raise RuntimeError('fixed POST receipt/persistence population mismatch')
    sequences = {message_id: sequence for sequence, message_id in acks}
    pairs = []
    for message_id, actual_room, actual_creator, client, body, indexed in rows:
        sequence = sequences.get(message_id)
        if (sequence is None or body != f'bench write {sequence}'
                or actual_room != int(room) or actual_creator != creator
                or client != f'{prefix}{sequence}'
                or (indexed or '').split() != body.split()):
            raise RuntimeError('fixed POST body/client/creator/room/FTS mismatch')
        pairs.append([sequence, message_id])
    if (sorted(acks) != sorted(pairs) or len({pair[0] for pair in pairs}) != expected
            or len({pair[1] for pair in acks}) != expected):
        raise RuntimeError('fixed POST acknowledged IDs/sequences mismatch')
    return {'count': expected, 'exact_ids_body_client_creator_room_fts': True}
