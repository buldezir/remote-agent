import { createRoot } from "react-dom/client";
import { ServerStore } from "./store/serverStore";
import { App } from "./ui/App";
import { settings } from "./ui/settings";
import "./ui/styles.css";

settings.apply();
settings.watchSystem();

createRoot(document.getElementById("root")!).render(<App store={new ServerStore()} />);
