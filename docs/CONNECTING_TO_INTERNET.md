# How to Connect Raspberry Pi to the Internet

## Find and Register MAC Address

### Choose a MAC Address

- Find a MAC address that works and register it on PacketFence
- Use a MAC address from a real device
- For PlayStation 5 spoofing, pick from this block:
  - Range: `E8:6E:3A:00:00:00` to `E8:6E:3A:FF:FF:FF`
- Select two addresses: one for ethernet and one for WiFi
- Register both via PacketFence

## Change Device MAC Address

### Set Temporarily (No Software Required)

This method is useful for installing macchanger:

```bash
sudo ip link set dev eth0 down
sudo ip link set dev eth0 address E8:6E:3A:00:00:01
sudo ip link set dev eth0 up
```

### Update Permanently

Edit the network configuration file:

```bash
sudo nano /etc/netplan/50-cloud-init.yaml
```

Add this configuration (modify existing file with macaddress fields):

```yaml
network:
  version: 2
  ethernets:
    eth0:
      optional: true
      dhcp4: true
      macaddress: e8:6e:3a:00:00:01 # Different from WiFi MAC address
  wifis:
    wlan0:
      optional: true
      dhcp4: true
      macaddress: e8:6e:3a:00:00:02 # Different from ethernet MAC address
      access-points:
        "pards":
          hidden: true
```

**Note:** Ensure ethernet and WiFi MAC addresses are different.

## Remote Access / SSH on Restricted Networks

### Setup Tailscale

1. Install Tailscale on both your computer and Raspberry Pi
2. Visit [tailscale.com](https://tailscale.com/) for installation instructions
3. SSH to the Tailscale IP address

### SSH Connection Example

```bash
ssh ubuntu@radioserver
```

Where `radioserver` is the Tailscale hostname (you can also use the IP address).

### Security Recommendation

Add an SSH public key to the `authorized_keys` file for key-based authentication instead of password authentication.

## Host Server from Raspberry Pi

### Network Limitations

Many institutional networks block most incoming connections, so you may need a tunnel solution to allow external access to your services.

### Recommended Solutions

**Cloudflare Tunnels (Recommended)**

- Requires a domain registered with Cloudflare

**Ngrok (Alternative)**

- Good for testing and development
- Usable for production but not recommended (gives warning to users)
- Free tier available with limitations

Both solutions will create a secure tunnel to expose your Raspberry Pi services to the internet despite network restrictions.
