import { useEffect, useState } from "react";
import * as api from "../api";
import type { Account as AccountData } from "../types";

export function Account() {
  const [account, setAccount] = useState<AccountData | null>(null);

  useEffect(() => {
    api.account().then(setAccount);
  }, []);

  return <pre>{account ? JSON.stringify(account) : null}</pre>;
}
