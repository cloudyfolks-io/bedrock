import { useState } from "react";
import * as api from "../api";
import type { Challenge } from "../types";

interface DeviceEntryProps {
  onChallenge: (next: Challenge) => void;
}

export function DeviceEntry({ onChallenge }: DeviceEntryProps) {
  const [userCode, setUserCode] = useState("");

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        api.startDevice(userCode).then(onChallenge);
      }}
    >
      <input value={userCode} onChange={(event) => setUserCode(event.target.value)} />
      <button type="submit">Continue</button>
    </form>
  );
}
