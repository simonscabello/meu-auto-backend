UPDATE maintenance_items SET history_question = NULL
WHERE owner_user_id IS NULL
  AND slug IN (
      'alinhamento',
      'balanceamento',
      'rodizio_pneus',
      'palhetas',
      'discos_freio',
      'amortecedores'
  );
