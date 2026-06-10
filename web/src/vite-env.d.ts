/// <reference types="vite/client" />

// G3: optuna-dashboard deep-link target, externalized from the old hardcoded
// VM IP. Empty → App.tsx falls back to the localhost tunnel default. Set at
// build time per deploy (see web/.env.example):
//   dev (VM, host browser):  VITE_OPTUNA_URL=http://192.168.67.129:8088/
//   mainnet (SSH tunnel):    VITE_OPTUNA_URL=http://localhost:8088/  (default)
interface ImportMetaEnv {
  readonly VITE_OPTUNA_URL?: string
}
