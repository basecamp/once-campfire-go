import sqlite3
import unittest

from acknowledged_writes import verify


class AcknowledgedWritesTest(unittest.TestCase):
    def database(self):
        db = sqlite3.connect(':memory:')
        self.addCleanup(db.close)
        db.executescript('''
            CREATE TABLE messages(id INTEGER PRIMARY KEY, room_id, creator_id, client_message_id);
            CREATE TABLE action_text_rich_texts(record_type, record_id, name, body);
            CREATE VIRTUAL TABLE message_search_index USING fts5(body);
            INSERT INTO messages VALUES(101, 42, 7, 'rate-fixture-0'), (102, 42, 7, 'rate-fixture-1');
            INSERT INTO action_text_rich_texts VALUES
                ('Message', 101, 'body', 'bench write 0'), ('Message', 102, 'body', 'bench write 1');
            INSERT INTO message_search_index(rowid, body) VALUES(101, 'bench write 0'), (102, 'bench write 1');
        ''')
        return db

    def sample(self):
        return {'ok': 2, 'client_message_prefix': 'rate-fixture-', 'acknowledged': [[1, 102], [0, 101]]}

    def test_exact_population_independent_of_completion_order(self):
        db = self.database()
        result = verify(db, 100, 42, 7, self.sample())
        self.assertEqual(result, {'count': 2, 'exact_ids_body_client_creator_room_fts': True})

    def test_receipt_mismatch_despite_matching_write_count(self):
        for acks in ([[0, 101]], [[0, 101], [0, 101]], [[0, 102], [1, 101]]):
            with self.subTest(acks=acks):
                sample = self.sample()
                sample['acknowledged'] = acks
                with self.assertRaises(RuntimeError):
                    verify(self.database(), 100, 42, 7, sample)

    def test_persisted_identity_and_body_are_not_count_only_checks(self):
        for sql in (
            'UPDATE messages SET room_id=43 WHERE id=101',
            'UPDATE messages SET creator_id=8 WHERE id=101',
            "UPDATE messages SET client_message_id='another-request' WHERE id=101",
            "UPDATE action_text_rich_texts SET body='bench write 9' WHERE record_id=101",
            "UPDATE message_search_index SET body='different body' WHERE rowid=101",
            'DELETE FROM action_text_rich_texts WHERE record_id=101',
            "UPDATE messages SET client_message_id='rate-fixture-00' WHERE id=101;"
            "UPDATE action_text_rich_texts SET body='bench write 00' WHERE record_id=101;"
            "UPDATE message_search_index SET body='bench write 00' WHERE rowid=101",
        ):
            with self.subTest(sql=sql):
                db = self.database()
                db.executescript(sql)
                with self.assertRaises(RuntimeError):
                    verify(db, 100, 42, 7, self.sample())


if __name__ == '__main__':
    unittest.main()
