-- 0.4.3: a card installment remembers its purchase (the full-price expense on the card), shown on the item's page.
ALTER TABLE recurrings
    ADD COLUMN purchase_transaction_id uuid REFERENCES transactions(id) ON DELETE SET NULL;
