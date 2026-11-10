-- The supplier role (3.0.39): opened, suspended and retired like the customer role, apart from it.
ALTER TABLE partner_role DROP CONSTRAINT partner_role_role_check;
ALTER TABLE partner_role ADD CONSTRAINT partner_role_role_check CHECK (role IN ('customer', 'supplier'));
