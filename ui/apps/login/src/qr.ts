import QRCode from "qrcode";

export function otpauthSVG(otpauthURL: string): Promise<string> {
  return QRCode.toString(otpauthURL, { type: "svg" });
}
