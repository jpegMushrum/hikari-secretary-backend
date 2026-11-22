package postgres

// AnnouncementLinkSQL resolves a sent Telegram message for event publication reference ref.
const AnnouncementLinkSQL = `(SELECT 'https://t.me/' ||
				CASE WHEN d.destination LIKE '@%' THEN substr(d.destination,2)
					ELSE 'c/' || (-peer.dialog_id - 1000000000000)::text END ||
				'/' || concat_ws('/',d.message_thread_id,d.external_id)
			 FROM publication_deliveries d
			 CROSS JOIN LATERAL (SELECT CASE WHEN d.destination ~ '^-[0-9]{13}$'
				THEN d.destination::bigint END AS dialog_id) peer
			 WHERE d.publication_id=ref.external_publication_id::bigint
				AND d.platform='telegram' AND d.status='sent' AND d.external_id ~ '^[1-9][0-9]*$'
				AND (d.destination ~ '^@[A-Za-z][A-Za-z0-9_]*$' OR peer.dialog_id BETWEEN -1997852516352 AND -1000000000001)
			 ORDER BY (d.destination LIKE '@%') DESC,d.id LIMIT 1)`
